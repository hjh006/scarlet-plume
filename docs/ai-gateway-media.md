# 私网媒体检测服务

`docker/media/Dockerfile` 构建独立的 `scarlet-media-inspector`，不改变默认网关部署。镜像包含 Tesseract 英文 OCR 与 whisper.cpp ASR。whisper.cpp 固定为 [v1.9.5 源码提交](https://github.com/ggml-org/whisper.cpp/tree/d1be6fde11ac6e0407606b4e42fe72d34add8037)，构建时核对提交 SHA；[官方构建定义](https://github.com/ggml-org/whisper.cpp/blob/d1be6fde11ac6e0407606b4e42fe72d34add8037/examples/cli/CMakeLists.txt)提供 `whisper-cli`。CPU 构建关闭本机专用指令集、OpenMP、模型下载和额外服务。

镜像不下载或内嵌语音模型。部署人员需取得已批准的模型，核验来源、许可与 SHA-256，再将文件以只读方式挂载为 `/models/ggml-model.bin`。启动时和每次 ASR 调用前均核对模型摘要；模型缺失、摘要不符、命令失败或超时均拒绝。Tesseract 此镜像仅安装 `eng`，其他语言需要单独增加已批准的语言包并验收。

## 范围与数据路径

- 图片：PNG、JPEG，经像素规范化后实际 OCR；拒绝未知编码、超限图片和远程媒体 URL。
- 音频：PCM16、单声道、16 kHz、最长 30 秒的 WAV；实际 ASR，不采用调用者或上游自报的转录结果。
- 目录策略：单体最多 4 MiB、总量最多 8 MiB、最多 1600 万像素及 16 个媒体资产。`Model.media` 必须明确允许对应 MIME，新配置采用 `inspectionVersion: scarlet-media-ocr-asr-v2`。
- 输出：规范化后的模型媒体同样扫描；流式媒体输出不在当前范围。文本流采用 `strict_hold` 完整检查后回放，不承诺首令牌低延迟。

运行时先规范化媒体，再将其通过已配置的私网 HTTPS 端点交给扫描器。扫描器在权限为 `0600` 的临时文件中执行工具，每次结束删除目录。示例容器将临时目录放在有界 tmpfs，根文件系统、模型和证书均只读，进程使用非 root UID。CLI与示例容器保持最多并发2个请求，超量返回503；直接嵌入 `NewMediaInspectionHandler` 的库实例最多4个请求，超量返回429。两者均不排队，返回 `Retry-After: 1`，取消或失败后释放容量；上游遇限流时拒绝处理媒体，不绕过检查。单个识别过程最多45秒；不会将媒体正文写入控制面审计。

OCR/ASR 是有明确格式与语言边界的检测能力，不保证理解所有视觉、语义或隐写信息。输入强制附加 `image` 或 `audio` 类别；未识别到文本或置信度低于 90 时附加 `unclassified_media`。模型与共同策略都需要明确批准这些类别；配置启用与节点上报不等于实际扫描验收。

v2取每个非空OCR词或真实ASR文字token的最低置信度，不能让大量高置信文字稀释一个低置信片段。版面结构、空白与ASR控制token不充当识别文字；没有有效识别文字时置信度为0，非有限或越界数值拒绝。最低值是保守的不确定性信号，不能证明未识别区域不含敏感信息。

历史v1采用不同的置信聚合算法，其回执和审计保留原版本，不回填或解释成v2最低值。已有v1目录仍可载入且不自动改写；新网关配合v2扫描器时可在原目录范围内执行，实际回执与审计均明确标记v2。实际执行只接受v2处理器回执；升级须同时采用新网关和新扫描器。显式迁移目录版本仍需保存、回归与独立发布，不能借迁移扩大权限。旧扫描器即使返回200与高置信度也被拒绝；输入执行返回403 `media_input_inspection_required`，不会抵达模型提供方。新扫描器本身拒绝旧版或无效处理器回执，返回422 `inspection_receipt_invalid`。

## 部署

准备仅部署进程可读的 TLS 私钥、完整证书链和已批准模型。证书 SAN 必须匹配运行时访问的扫描器主机名；运行时容器必须信任签发 CA，不关闭证书校验。非 root UID `10001` 必须有读取证书、模型的权限。

通过部署环境提供以下变量，不提交真实值：

| 变量 | 用途 |
| --- | --- |
| `SCARLET_MEDIA_INSPECTOR_TOKEN` | 私网 Bearer token，32—512 字符，不包含换行 |
| `SCARLET_MEDIA_INSPECTOR_MODEL_SHA256` | 已批准模型的 64 位小写 SHA-256 |
| `SCARLET_MEDIA_INSPECTOR_TLS_DIR` | TLS 目录，默认 `./var/media-inspector/tls` |
| `SCARLET_MEDIA_INSPECTOR_MODEL_DIR` | 模型目录，默认 `./var/media-inspector/models` |
| `SCARLET_MEDIA_INSPECTOR_NETWORK` | 私网名称，默认 `scarlet-media-inspection` |

TLS 目录需包含 `tls.crt`、`tls.key`；模型目录需包含 `ggml-model.bin`。随后在仓库根目录执行：

```sh
docker compose -f compose.media-inspector.yaml --profile media-inspector build
docker compose -f compose.media-inspector.yaml --profile media-inspector up -d
```

该可选 profile 不映射宿主机端口，服务仅加入 `internal: true` 私网。需要调用扫描器的网关或运行时服务也必须加入同名网络；不要将扫描器网络改为公网出口来解决连通性。运行时采用固定端点 `https://media-inspector:8912/v1/inspect`，并由其部署环境配置对应 token。运行时原有控制面与上游所需网络可同时保留。

服务提供 `POST /v1/inspect`，要求 Bearer token 与媒体摘要绑定。`GET /healthz` 仅返回 `204`，不返回配置、模型路径或秘密；进程健康不证明某种媒体扫描成功。启动默认监听 `127.0.0.1:8912`；生产示例显式监听容器私网并配置 TLS。

本地测试可显式使用 `--local --listen 127.0.0.1:8912` 允许 loopback 明文，不用于容器跨主机通信或生产部署。默认禁止非 TLS 的非 loopback 监听。CLI 支持 `--tls-cert`、`--tls-key`、`--tesseract`、`--whisper`、`--whisper-model`、`--whisper-model-sha256`、`--languages` 和 `--token-env`；不把 token 放进命令行参数。

## 验收

至少核对：已批准 PNG/JPEG 与 WAV 的真实允许请求、含敏感文本或语音的拒绝、未识别文本的保守分类、模型摘要不符、扫描器不可用、超时、错误 MIME 和超限内容。检查每条回执的 `mediaDigests` 与 `mediaInspectionVersion`，确认被拒绝媒体没有抵达上游。不要将构建成功、`/healthz` 成功、配置发布或节点能力上报当作检测验收完成。
