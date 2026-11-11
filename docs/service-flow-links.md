# 跨服务调用关系

运行时数据流可以跨采集批次、跨来源关联两端的 HTTP Span。此功能以已接入的受控观测为依据；并不通过相同数据类别、相同接口名或共享 Trace 猜测应用依赖。

一次 `service_call` 关系要求同时满足：

1. 两端来自同一工作区、不同且当前有效的 OTLP 来源，来源创建人仍具有 Owner 角色。
2. A 的 HTTP CLIENT 子 Span 在导出批次内能够追溯到 A 的 SERVER Span。
3. A CLIENT 的显式 `peer.service` 等于 B 已登记来源的 Service 别名。
4. A 与 B 的 Trace ID 相同；B SERVER 的原生 ParentSpanId 等于该 A CLIENT SpanId。
5. 两端观测均在最近7天内，结束时间相差不超过2分钟。

Trace/Span 的长度和非零约束遵循 [W3C Trace Context](https://www.w3.org/TR/trace-context/)。父子关系来自 [OpenTelemetry SpanContext](https://opentelemetry.io/docs/specs/otel/trace/api/#spancontext)，目标名称仅采用显式的 [peer.service 属性](https://opentelemetry.io/docs/specs/semconv/registry/attributes/peer/)，不由原始 URL、Host、IP、SQL 或请求内容推导。服务别名应由组织统一配置，不包含个人信息或业务原值。

规范化投影新增 `parentSpanRef` 及最多32项 `serviceCalls: [{targetService, spanRef}]`，数组排序、去重、克隆后计算重放摘要；相同客户端 Span 不得指向两个不同服务。空字段省略，原有无调用字段的批次摘要保持一致。超过源端32项时保留显式覆盖警示。无对应接收端时，调用保留在请求调查的候选元数据，显示缺少双端证据，不生成关系。

服务调用边连接两端已观测 API，包含双方来源ID、记录、Trace/Span、两个批次、父子引用和时间窗。双端证据不加总为请求数。由某个服务筛选时，仅沿已证实的调用关系保留邻接 API 和数据库上下文；去标识主体仍按来源隔离，不因 Trace 相同合并身份。表级关系另外要求目标系统、采集来源、数据库、Schema、Table 精确匹配真实有效资产目录；没有匹配时只能显示数据库类型及缺口。

每次最多读取500个服务调用配对或未配对候选。超限明确返回范围过大，自动快照记录失败状态并保留原采集；缩小服务或 Trace 范围后重试。两个观测时钟应同步；异步任务、消息队列、Span Links、仅一端采样或超过时间窗的慢调用不会被当作已证实的 HTTP 父子调用。没有完整插桩与覆盖证据时，不将局部图描述为全部业务链路。

## 本地双服务样例

`compose.service-sample.yaml` 使用同一个 Go 样例镜像启动两个明确插桩的合成应用：

- A：`GET /api/chain`，真实读取 `inventory.classification_contacts`。
- A 通过 HTTP 传播 `traceparent` 调用 B：`GET /api/orders`，B 真实读取 `inventory.orders`。
- A 导出 SERVER、DB CLIENT、HTTP CLIENT；B 单独导出 SERVER、DB CLIENT，使用不同来源令牌、不同 OTLP HTTP 批次。

先为两个应用创建 OTLP 来源，将 B 的已登记 Service 别名精确填入 `SCARLET_SERVICE_B_ALIAS`。在已忽略的本地环境文件配置下列值，不提交凭据：

```text
SCARLET_SERVICE_PASSWORD=<随机的至少16字符样例口令>
SCARLET_SERVICE_A_OTLP_TOKEN=<A来源令牌>
SCARLET_SERVICE_B_OTLP_TOKEN=<B来源令牌>
SCARLET_SERVICE_B_ALIAS=<B已登记Service别名>
SCARLET_FLOW_DATA_SOURCE=<已有PostgreSQL采集来源别名>
SCARLET_SAMPLE_READER_PASSWORD=<已有样例reader口令>
```

```sh
docker compose --env-file .env.local \
  -f compose.local.yaml -f compose.sample.yaml -f compose.service-sample.yaml \
  up -d --build service-sample-a service-sample-b
```

以浏览器访问 `http://127.0.0.1:8894/api/chain`，Basic Auth 用户名为 `scarlet-demo`，密码为本地配置的样例口令。响应包含实际计数和 Trace 引用，可在数据流转页按该 Trace 核验两来源、父子关系及精确表级依据。8895端口可单独访问 B；没有 A 的 CLIENT Span 时，独立访问不会凭空建立 A→B 关系。样例 HTTP 仅绑定本机端口，服务间位于本地 Docker 网络；它是明确插桩演示，不代表反向代理自动获得了所有应用内部调用。

真实 PostgreSQL 与 HTTP 验证：

```sh
cd backend
PLUME_STORE_TEST_DSN='postgres://localhost/disposable_test' \
  go test -race ./internal/runtimesecurity ./internal/store ./cmd/service-sample \
  -run 'TestService|TestOTLPHTTP|TestRuntimeCaptureReplayIdentity' -count=1
```
