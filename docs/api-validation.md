# 受限 API 授权验证

本模块由平台批准限定计划、客户侧 runner 实测，再将源端结论接入已有 G03 检查、整改和独立复核。首版只执行明确登记的私网或 loopback Origin、GET 模板、两个已授权测试账号及已知合成对象。没有对象枚举、口令猜测、爆破、公网扫描或浏览器打标。

## 验证与判断

| 规则及固定版本 | 源端验证 | 平台接受的结论 |
| --- | --- | --- |
| `bola` v1 | A读取A自有对象；B读取B自有对象；B请求A对象。前三个账号/对象事实由本地 manifest 明确给定，响应对象标识和所有者在源端比较。 | 只有前两步成功且账号不同、第三步返回同一已知A对象，才记录 `vulnerable`。A、B自有对象均确认后，第三步明确403/404且未返回A对象，才记录 `not_observed`。普通200、不同对象、不完整响应或认证不明均为 `inconclusive`。 |
| `weak_credential` v1 | 只使用客户显式提供的A测试账号和口令请求其已知自有对象一次，确认账号确实成功认证。源端比较已提供口令是否少于12个Unicode字符，或精确命中内置4项常见值。 | 成功认证并命中组织规则才记录 `vulnerable`；使用当前已提供的强口令重新成功认证且不命中，才能记录 `not_observed`。口令无效、账号停用、对象不明均为 `inconclusive`。不尝试任何其他口令。 |

弱口令阈值是组织策略 v1，不声称是法定最低长度或完整口令强度评估。结果仅覆盖所选账号、对象和时间，不证明全部账号、接口或所有认证机制安全。本版认证方式为 Basic Auth；不自动登录、抓取或更新生产会话。

对象级授权与认证风险的分类参考 [OWASP API1:2023](https://api-security.owasp.org/editions/2023/en/0xa1-broken-object-level-authorization/) 和 [OWASP API2:2023](https://api-security.owasp.org/editions/2023/en/0xa2-broken-authentication/)。平台把业务授权前提、源端验证和独立复核保留为各自可核验的步骤。

## 双侧范围与审批

Owner / Operator 配置来源、服务别名、`GET` route模板、精确 Origin、本地 `manifestRef` 和5—43200分钟周期（默认60分钟）。只有未撤销的 `security_events` 来源可使用；其创建人不再是 Owner 时，领取与回执立即失效。

Owner / Reviewer 独立批准计划；创建人和任何编辑过该计划的人员不能批准。修改范围、来源、引用、规则或周期后，计划修订增加、审批清除，旧租约失效；相同输入保存不增加修订。批准会使计划进入自动调度，源端每10秒领取一个到期任务。界面支持“测试连接”和“立即验证”；连接诊断也需要批准，仅检查来源端连通条件，永远不生成风险消除或已证实漏洞结论。

客户侧 manifest 再次限定精确 `sourceId`、Origin、route、规则及凭据引用。A、B的账号不同；账号、口令、实际对象路径、已知对象标识、所有者标识均留在源内。manifest 的引用应采用不可变版本命名；改变账号范围或对象集合时，创建新引用并重新批准，口令轮换保持原账号和对象范围。

runner 每次连接解析目标地址并固定到允许的私网或 loopback IP，拒绝公网、link-local、未指定地址及混合公网解析，不使用系统 HTTP 代理、不跟随重定向。HTTPS 使用正常主机名验证，最低 TLS1.2；可通过 `-target-ca` 加载私有 CA，不提供跳过校验选项。每个响应最多读取64KiB，单请求3秒，单轮探测最多15秒；两规则合计最多4个已给定请求，无字典或对象循环。

## 证据与闭环

平台只接收计划/修订/租约、观察时间、规则、状态码、布尔比较结果、请求数及白名单缺口代码；原始响应、口令和真实对象值不上传。服务端重新计算结论，无法用 `outcome: vulnerable` 或单个200绕过证明条件。租约有效期2分钟，目标、来源、模式和修订均绑定；不同内容重放返回409。

`vulnerable` 通过已有安全事件 pipeline 生成 G03 和原生观察证据。完成整改记录后，同一计划版本的已完成源端负验证才可产生 `cleared` / 原生复测证据。首次负验证没有对应风险时只留运行记录；`inconclusive`、源端失败和连接诊断不会发送 `cleared`。源端结果不自动通过独立复核。

事件 rule 固定包含计划ID、修订、规则和v1版本。作用域变化后的新版本不会清除旧版本检查。每个事件ID由固定运行引用与规则生成；结果先保存，事件投影失败显式标记 `projectionState=error`。相同结果可幂等重试，编辑角色也可在运行列表点击“重试检查汇总”。runner 在本地0600文件暂存不含敏感原值的结果，网络失败重试同一内容；不会把汇总失败伪装为检查成功。

每工作区最多100计划，每计划保留最近200条已结束运行（界面100条），固定采用计划快照。JSON / HTML 报告含来源、目标、计划版本、规则版本、观察时间、布尔证明、状态码、缺口、汇总状态、事件引用与整改检查编号。检查详情继续显示其当前独立复核状态；历史导出不是对所有账号、对象或业务的完整安全认证。

## 本地 Docker 样例

`compose.validation-sample.yaml` 提供 `api-validation-sample` 和 `api-validator`，镜像由 `docker/api-validation/Dockerfile` 构建。样例使用已有 `sample-db` 的 `scarlet_sample`、`sample_reader`，真实执行 `SELECT id FROM inventory.customers WHERE id=$1`，仅返回两个合成对象及合成所有者，不返回客户个人信息。

1. 在平台安全运营中创建 `security_events` 来源。令牌只写入已忽略的本地环境文件。
2. 创建已忽略的 `.scarlet-validation` 目录，将 [manifest示例](../docker/api-validation/manifest.example.json) 复制为其中的 `manifest.json`，填入该来源实际ID及本地测试账号对应的所有者。示例路径只使用合成 `fixture-a` / `fixture-b`。
3. 配置以下环境变量（值不提交仓库）：

```text
SCARLET_VALIDATION_SOURCE_TOKEN=<security_events来源令牌>
SCARLET_VALIDATION_A_USER=validation-a
SCARLET_VALIDATION_A_PASSWORD=<客户提供的A测试口令>
SCARLET_VALIDATION_B_USER=validation-b
SCARLET_VALIDATION_B_PASSWORD=<客户提供的B测试口令>
SCARLET_SAMPLE_READER_PASSWORD=<已有样例reader口令>
```

```sh
docker compose --env-file .env.local \
  -f compose.local.yaml -f compose.sample.yaml -f compose.validation-sample.yaml \
  up -d --build api-validation-sample api-validator
```

样例端口仅绑定 `127.0.0.1:8896`。`/api/secure/objects/{id}` 始终检查所有权；`/api/vulnerable/objects/{id}` 仅当源内 `.scarlet-validation/mode` 文件明确包含 `vulnerable` 时启用故意缺陷，缺失或其他内容默认安全。将文件改为 `fixed` 后，下一次请求立即使用安全模式，不提供网络端模式切换接口。

平台计划使用 Origin `http://api-validation-sample:8080`、模板 `/api/vulnerable/objects/{id}`、引用 `synthetic-objects-v1`。独立批准后可立即验证。复现 BOLA 闭环时可先只选择 `bola`：真实发现→登记整改→源内模式改为fixed→立即验证得到强负结果→独立复核。弱凭据闭环需要将已提供的A口令实际轮换为强口令，并同步源内环境及重启两个容器，再确认新口令成功认证；仅拒绝旧口令不当作全部认证条件已核实。

## HTTP 与 CLI

浏览器接口前缀 `/api/v1/workspaces/{slug}/api-validation`：

| 接口 | 用途 |
| --- | --- |
| `GET/POST /plans`、`PUT /plans/{id}` | 列表、创建、带revision更新 |
| `POST /plans/{id}/approve` | 独立批准 `{revision}` |
| `POST /plans/{id}/request` | `{revision,mode:validation/connection}` |
| `POST /plans/{id}/disable` | 停用并撤销租约 |
| `GET /plans/{id}/runs` | 运行、缺口、检查ID与汇总状态 |
| `POST /plans/{id}/runs/{runRef}/retry` | 幂等补偿安全事件汇总 |
| `GET /plans/{id}/export?format=json/html` | 固定版本证据报告 |

runner 使用专用 `POST /api/v1/api-validation/claim` 与 `/result`，Bearer用途限 `security_events`，领取体必须含本地 `sourceId`。两接口仍执行来源、SourceOwner、工作区、批准、租约与重放校验。

```sh
SCARLET_VALIDATION_SOURCE_TOKEN='<source-local-token>' scarlet-api-validator \
  -url https://scarlet.example.internal \
  -manifest /customer-config/manifest.json -state /customer-state/pending.json
```

`-once` 只领取一次；`-local` 允许本地 HTTP 平台地址，不关闭目标 HTTPS 校验。接入真实业务前需要客户实际提供范围明确的测试账号、合成对象和对应本地 manifest；默认配置不能主动探测任意来源。
