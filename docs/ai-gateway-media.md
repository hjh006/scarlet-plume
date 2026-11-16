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
| `SCARLET_GATEWAY_SCANNER_CA_FILE` | 网关同步部署使用的扫描器公共 CA 文件，建议绝对路径 |

TLS 目录需包含 `tls.crt`、`tls.key`、`ca.crt`；模型目录需包含 `ggml-model.bin`。`ca.crt` 是健康检查使用的公共 CA，不能包含私钥。随后在仓库根目录执行：

```sh
docker compose -f compose.media-inspector.yaml --profile media-inspector build
docker compose -f compose.media-inspector.yaml --profile media-inspector up -d
```

该可选 profile 不映射宿主机端口，服务仅加入 `internal: true` 私网。需要调用扫描器的网关或运行时服务也必须加入同名网络；不要将扫描器网络改为公网出口来解决连通性。运行时采用固定端点 `https://media-inspector:8912/v1/inspect`，并由其部署环境配置对应 token。运行时原有控制面与上游所需网络可同时保留。

服务提供 `POST /v1/inspect`，要求 Bearer token 与媒体摘要绑定。`GET /healthz` 仅返回 `204`，不返回配置、模型路径或秘密；进程健康不证明某种媒体扫描成功。启动默认监听 `127.0.0.1:8912`；生产示例显式监听容器私网并配置 TLS。

本地测试可显式使用 `--local --listen 127.0.0.1:8912` 允许 loopback 明文，不用于容器跨主机通信或生产部署。默认禁止非 TLS 的非 loopback 监听。CLI 支持 `--tls-cert`、`--tls-key`、`--tesseract`、`--whisper`、`--whisper-model`、`--whisper-model-sha256`、`--languages` 和 `--token-env`；不把 token 放进命令行参数。

## 同步升级与就绪

同时部署网关与扫描器时，在原网关所需环境变量之外提供上表的扫描器变量，以及 `SCARLET_GATEWAY_SCANNER_CA_FILE`。网关私有配置目录需有与扫描器环境相同的 `media-inspector.token`（0600）。使用同步覆盖文件：

```sh
docker compose -f compose.ai-gateway.yaml -f compose.media-inspector.yaml \
  -f compose.ai-gateway-media.yaml --profile media-inspector build
docker compose -f compose.ai-gateway.yaml -f compose.media-inspector.yaml \
  -f compose.ai-gateway-media.yaml --profile media-inspector up -d
```

升级保留 `/state` 的审计和独占 nonce journal、控制面数据库、既有预算与未决费用；不要通过删除状态文件或重放已消费请求恢复健康。镜像标签包含 `media-v2`，实际检测还须通过就绪探针与真实业务媒体验收，标签本身不证明协议兼容。

实际媒体请求仍受应用预算、RPM、TPM 和输出上限约束。控制面按输入字节与识别投影做保守预留，不能用模型供应商的文字 token 估算替代；WAV 的 base64 请求可能超过为短文本设置的 TPM。应在保留安全策略的前提下通过受审计的配置更新设定明确额度，不清空计数或跳过准入来使验收通过。

扫描器 healthcheck 使用 `--check-url` 发起认证后的真实 PNG OCR 与静音 WAV ASR，要求返回当前 v2 协议，并验证摘要绑定。该模式不启动服务，也不调用模型。可使用 `--check-formats image/png` 检查只启用 OCR 的独立部署；默认组合要求 OCR 和 ASR 都可用。`--scanner-ca-file` 为探针追加专用 CA，保留系统根证书和证书主机名校验；错误 CA、主机名不符、旧 v1 回执、认证错误、工具故障或超时均失败。单轮最多 50 秒，没有并行重试队列。

网关在开放 HTTPS 入口前，对已启用目录要求的媒体格式运行同类真实探针，JPEG 对应规范化 PNG；每轮完成后间隔 30 秒复查。探针仅含固定白色像素与静音 PCM，不提交模型请求、不进行预算准入、不向控制面发送媒体正文。就绪结果超过 90 秒或依赖失败时变为不可用，节点 heartbeat 反映 `degraded`。网络或 DNS 故障可能耗尽单轮截止，状态不会承诺在停机瞬间更新。恢复成功后重新变为就绪，不清理 journal 或预算。无媒体模型的网关不要求媒体探针。

网关 `-health-listen` 默认为容器内 `127.0.0.1:8906`，不得映射为公网服务；同一宿主运行多个原生进程时各自分配不同端口。该私有监听上 `/healthz` 只证明进程存活，`/readyz` 只返回 204 或 503。`ai-gateway -check-ready` 查询已有就绪结果，在读取目录、凭据、bindings 或 journal 前退出，不重启运行时或执行真实请求。每次健康检查只读已有结果，不产生额外扫描。

扫描器自身的定时 healthcheck 和网关探针可能与业务竞争 CLI 的两个扫描槽位；繁忙时保守地报告不可用或 `degraded`，不会绕过检查。健康状态不是接入流量的唯一授权门禁，执行中的请求仍逐次完成真实检查，扫描器故障时媒体请求失败关闭。就绪成功不证明 OCR/ASR 识别质量、供应商可达、客户请求成功或输出安全。

网关 `-media-inspector-ca-file`（或 `SCARLET_GATEWAY_MEDIA_INSPECTOR_CA_FILE`）只为扫描器客户端追加公共 CA，不改变控制面或模型供应商的系统信任。若私有控制面另用专用 CA，应独立配置完整的系统根加私有 CA 信任包，不可用扫描器 CA 覆盖系统根。

## 验收

至少核对：已批准 PNG/JPEG 与 WAV 的真实允许请求、含敏感文本或语音的拒绝、未识别文本的保守分类、模型摘要不符、扫描器不可用、超时、错误 MIME 和超限内容。检查每条回执的 `mediaDigests` 与 `mediaInspectionVersion`；输入媒体被拒绝时确认未送往上游，输出媒体被拦截时确认未交付客户端，同时保留已经执行的上游调用与未知费用状态。不要将构建成功、`/healthz` 成功、配置发布或节点能力上报当作检测验收完成。
