# API 运行时观测

在「API 安全 → 运行时观测」中，赤翎按观测来源、HTTP 方法和路由模板汇总请求数、4xx、5xx 和最近观测时间，并按服务别名对照最新 OpenAPI 契约。可以看到契约已记录、契约未记录、已弃用但仍有流量，以及无匹配契约的情况。契约未记录是一条核对线索，判定范围取决于已接入的流量和契约。当前运行时自动化止于采集、统计和契约对照。检查、证据、整改和报告归档仍按各自现有流程处理。

## 接入架构

| 接入方式 | 部署位置 | 观测范围 |
| --- | --- | --- |
| 独立 Go Agent | 服务入口，反向代理到固定上游 | 同一个 `scarlet-runtime-agent` 可连接 Java、Python、Go 等 HTTP 服务，应用侧只需保持正常的 HTTP 接口。Agent 根据配置的受信任路由模板识别请求 |
| 既有网关日志，可选 | 网关终止 TLS 并匹配路由之后 | 将网关的匹配模板、方法、响应状态、时间和事件 ID 投递到同一个运行时接入端点。支持把约定的 NDJSON 送给 `scarlet-gateway-log` |
| OpenTelemetry 追踪，可选 | 应用或客户侧 Collector | 接收 HTTP SERVER span 的路由和状态，以及在同一个导出批次内沿父链关联的数据库 CLIENT span 系统类型。支持不同语言的标准 OTLP HTTP protobuf 发送端。数据库关联受探针、采样和同批父链覆盖范围影响。 [OTLP 规范](https://opentelemetry.io/docs/specs/otlp/) · [HTTP span 约定](https://opentelemetry.io/docs/specs/semconv/http/http-spans/) · [数据库 span 约定](https://opentelemetry.io/docs/specs/semconv/db/database-spans/) |

独立 Agent 只向赤翎发送方法、已配置的路由模板、状态码、观测时间和随机事件 ID。实际路径参数、查询参数、请求头、Cookie、请求体、响应体和 TLS 私钥留在业务传输或源端。队列上限为 512 条，每批最多 100 条。短时重试沿用相同批次和事件 ID。队列满或重试耗尽会丢弃观测，业务请求仍由反向代理处理。界面按来源分别计数，同一次请求可能同时出现在 Agent 和 OTLP 来源中。

独立 Agent 覆盖经过代理且匹配配置模板的 HTTP 流量。已有网关接入可在现有转发链路上提供同类元数据。生产环境的旁路流量镜像及 OpenTelemetry eBPF Instrumentation（OBI）属于下一阶段，需按内核、运行时、TLS 终止位置和权限范围验证覆盖率、性能及采集字段。 [OpenTelemetry OBI 文档](https://opentelemetry.io/docs/zero-code/obi/)

后续接口风险监测的用例设计可参考 GB/T 46796-2025《数据安全技术 数据接口安全风险监测方法》。 [全国标准信息公共服务平台](https://std.samr.gov.cn//gb/search/gbDetailed?id=4507EFE13D6CCB6AE06397BE0A0A601F)

## 部署独立 Agent

工作区负责人在「运行时观测」点击「新增观测来源」，接入类型选择「独立采集 Agent / 网关日志」。服务别名应与对应 OpenAPI 扫描的来源名称一致。每个来源签发自己的令牌，部署时通过 `SCARLET_RUNTIME_GATEWAY_TOKEN` 注入。撤销后该来源停止上报，已有观测仍可查看。

从 `backend/` 运行以下示例。将域名、证书路径和路由替换为实际配置，通过部署端的凭据配置提供 `SCARLET_RUNTIME_GATEWAY_TOKEN`。

```sh
go run ./cmd/runtime-agent \
  -listen 0.0.0.0:8443 \
  -upstream https://orders.internal:8443 \
  -url https://scarlet.example.com \
  -tls-cert /run/scarlet-tls/server.crt \
  -tls-key /run/scarlet-tls/server.key \
  -upstream-ca /run/scarlet-tls/upstream-ca.pem \
  -route '/v1/orders/{id}' \
  -route '/v1/orders/status'
```

- `-listen` 是业务监听地址。构建后的本地镜像自带 `scarlet-runtime-agent` 命令。上游和平台配置使用固定 HTTP(S) 地址，源端模板用可重复的 `-route` 指定。
- `-tls-cert` 和 `-tls-key` 成对配置，用服务所有方授权的证书与私钥在源端终止 HTTPS，支持 TLS 1.2 及以上与 HTTP/2。需要客户端证书时添加 `-tls-client-ca /run/scarlet-tls/client-ca.pem`。将密钥作为只读挂载提供，并授权 Agent 用户读取。
- `-upstream` 使用 HTTPS 时校验上游证书和主机名。私有 CA 用 `-upstream-ca` 指定，公开可信证书使用系统信任库。平台上报默认要求 HTTPS。受限本地样例通过 `-local` 允许向回环地址或 Docker 服务 `app` 使用 HTTP。
- TLS 透传时 HTTP 方法、路由和状态保持加密，应在获授权的终止端或应用侧获取观测。字段级密文的语义取证需要合法的业务解密点。独立 Agent 的上报范围始终是路由级元数据。 [TLS 1.3 规范](https://www.rfc-editor.org/info/rfc9846)

## 接入已有网关或 OTLP

已有网关日志在源侧转换为每行一个 JSON 对象，只保留下面字段。路由必须来自已匹配的配置模板。观测时间使用 RFC 3339，事件 ID 由每次事件生成的 32 个小写十六进制字符组成。

```json
{"method":"GET","route":"/v1/orders/{id}","status":200,"observedAt":"2026-11-10T08:00:00Z","eventId":"b7ee1d4e35ab4903a8c83a59b61248b8"}
```

从 `backend/` 启动转发器。转发器通过工作区来源令牌向平台的 `POST /api/v1/runtime/gateway` 提交约定的 JSON 数组。已有网关也可直接使用同一认证和数组格式。

```sh
go run ./cmd/gateway-log -url https://scarlet.example.com < /path/to/normalized-observations.ndjson
```

需要内部数据库类型关联时，在「运行时观测」另建「应用探针（OTLP）」来源。以下变量用于支持标准配置的导出端。使用真实业务数据时，可在客户侧 Collector 先过滤属性和事件，因为直接发送的 OTLP 请求可能带有应用采集的原始属性。平台归一化和存储只使用可接受的路由、方法、状态、时间及数据库系统类型。

```sh
export OTEL_EXPORTER_OTLP_TRACES_PROTOCOL=http/protobuf
export OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=https://scarlet.example.com/api/v1/runtime/otlp/v1/traces
export OTEL_EXPORTER_OTLP_TRACES_HEADERS="Authorization=Bearer%20${SCARLET_RUNTIME_OTLP_TOKEN:?Set the OTLP source token}"
export OTEL_BSP_MAX_EXPORT_BATCH_SIZE=128
```

OTLP 接收端支持未压缩或 gzip 的 HTTP protobuf，每批最多 250 条符合条件的 HTTP 观测。只有带有效 `http.route` 的 HTTP SERVER span 能形成路由观测。数据库类型要求数据库 CLIENT span 与 HTTP SERVER span 的父链在同一接收批次内可追溯。 [OTLP 导出配置](https://opentelemetry.io/docs/specs/otel/protocol/exporter/)

## 本机 Docker 样例

从仓库根目录执行。先按 README 和[样例库步骤](automation.md#在工作台管理-postgresql-采集)准备 `.env.scarlet-local`、数据库密码、工作区和登录账号。升级旧环境时先构建新版平台镜像并运行数据库迁移，再登录工作台签发来源令牌。

```sh
scarlet_base() { docker compose --env-file .env.scarlet-local -f compose.local.yaml -f compose.sample.yaml "$@"; }
scarlet_base build app sample-db
scarlet_base up -d --wait db sample-db
scarlet_base run --rm -T app -local-docker -mode migrate
scarlet_base up -d --wait app
```

在默认地址 `http://127.0.0.1:8890` 登录。创建下列四个来源，把对应令牌填入本机的 `.env.scarlet-runtime`。同一业务服务的 Agent 和 OTLP 来源使用相同的服务别名，来源名称分别填写。

| 令牌变量 | 接入类型 | 示例服务别名 |
| --- | --- | --- |
| `SCARLET_RUNTIME_EDGE_JAVA_TOKEN` | 独立采集 Agent / 网关日志 | 赤翎 Java 运行时样例 |
| `SCARLET_RUNTIME_EDGE_PYTHON_TOKEN` | 独立采集 Agent / 网关日志 | 赤翎 Python 运行时样例 |
| `SCARLET_RUNTIME_JAVA_TOKEN` | 应用探针（OTLP） | 赤翎 Java 运行时样例 |
| `SCARLET_RUNTIME_PYTHON_TOKEN` | 应用探针（OTLP） | 赤翎 Python 运行时样例 |

首次运行生成回环测试证书。样例目录和环境文件由 Git 忽略。Linux 宿主机需保证挂载后的证书和私钥可由镜像中的 UID 10001 读取，并限制宿主机上的密钥访问。

```sh
umask 077
mkdir -p .scarlet-runtime-tls
if [ ! -e .scarlet-runtime-tls/server.crt ] && [ ! -e .scarlet-runtime-tls/server.key ]; then
  openssl req -x509 -newkey rsa:3072 -sha256 -nodes -days 7 \
    -subj '/CN=localhost' -addext 'subjectAltName=DNS:localhost,IP:127.0.0.1' \
    -keyout .scarlet-runtime-tls/server.key -out .scarlet-runtime-tls/server.crt
fi
chmod 600 .env.scarlet-runtime
test -s .scarlet-runtime-tls/server.crt && test -s .scarlet-runtime-tls/server.key

scarlet_runtime() { docker compose --env-file .env.scarlet-local --env-file .env.scarlet-runtime -f compose.local.yaml -f compose.sample.yaml -f compose.runtime.yaml "$@"; }
scarlet_runtime up -d --build --wait
```

两个入口运行同一 Agent 镜像并使用同一 TLS 测试证书，各自指向 Java 和 Python 样例。默认 HTTPS 端口为 `8891` 和 `8892`，仅绑定本机回环地址。端口可通过 `SCARLET_RUNTIME_JAVA_PORT` 和 `SCARLET_RUNTIME_PYTHON_PORT` 调整。

```sh
cert=.scarlet-runtime-tls/server.crt
curl --fail --cacert "$cert" https://127.0.0.1:8891/runtime/customers/123
curl --fail --cacert "$cert" https://127.0.0.1:8891/runtime/undocumented
curl --fail --cacert "$cert" https://127.0.0.1:8892/runtime/python/orders/123
curl --cacert "$cert" -sS -o /dev/null -w '%{http_code}\n' https://127.0.0.1:8892/runtime/python/legacy
```

最后一条预期返回 410。等待导出后刷新「运行时观测」，核对两个 Agent 来源的路由模板和状态统计。Java 应用探针可在样例查询成功时关联 `postgresql`，独立 Agent 的数据库系统栏显示空白。

若要验证契约对照，在 API 安全页分别以服务别名为来源名称上传 [Java 样例契约](../examples/openapi-runtime-java.json) 和 [Python 样例契约](../examples/openapi-runtime-python.json)。Java 契约声明 `GET /runtime/customers/{id}`，保留 `/runtime/undocumented` 作为未登记样例。Python 契约声明 `GET /runtime/python/orders/{id}`，将 `GET /runtime/python/legacy` 标记为 `deprecated: true`。回到运行时页面核对普通、契约未记录和弃用后仍被调用三类状态。以服务别名和最新契约为准，示例请求与计数随本机采样变化。
