# Bifrost OSS 与赤翎企业治理

网关使用固定版本的 Bifrost OSS 处理模型协议和转发，赤翎负责调用身份、数据与执行授权、预算以及证据。Bifrost 不直接向业务应用开放。具体镜像、固定上游配置和运行命令见 [Bifrost 网关文档](ai-gateway-bifrost.md)。

## 执行路径

```text
应用 / Agent
  → Scarlet AI Gateway：受支持的 OpenAI 文本接口、受控 MCP、/v1/execute
  → 工作区凭证解析与 PostgreSQL 原子占额
  → 现有 aisecurity Runtime：数据检查、RAG ACL、模型/地域策略、工具精确授权与审批
  → 本机私有 Bifrost：固定模型目的端
  → 模型服务
  → 输出检查、当前授权复核、持久审计、治理回执
```

RAG 与工具仍经现有 Runtime 执行。客户端不能指定上游 URL、供应商密钥、备用通道或 Bifrost 管理头。安全规则、目录哈希和真实运行配置共同约束路由。当前支持文本流式安全缓冲、受限图片/PCM 音频检查、加密精确缓存及逐路线重新授权的跨供应商切换。完整响应检查后才回放流式输出；任意 OpenAI 参数、未支持的媒体/协议和自动 Agent 执行仍拒绝。受控 MCP 的现代发现、工具列表、执行 nonce 与批准契约见 [MCP 接口](ai-gateway-mcp.md)。

## 已实现的控制面

入口为 `#/工作区/ai-gateway`，API 为 `/api/v1/workspaces/{slug}/ai-gateway`。

| 能力 | 实际执行方式 |
| --- | --- |
| 应用与调用权限 | 工作区隔离的应用、主体、模型与工具白名单；RAG 单独开关。所有范围与 Runtime 目录再做交集。 |
| 凭证 | 管理端随机凭证与签名工作负载兑换的短期凭证均仅返回一次；数据库保存 SHA-256、显示前缀及有效期。每次采用检查启用和到期状态。 |
| 工作负载身份 | Owner 导入静态公开 JWKS，精确绑定签发者、受众、主体与已保存应用；合格 JWT 一次兑换最长 300 秒的网关凭据，继续经过原有任务、目录和数据授权。 |
| 管理权限 | Owner 保存网关和身份配置；Owner/Operator 管理已批准应用的凭证；只读角色不能写配置或创建凭证。 |
| 版本绑定 | 启用配置必须提供实际安全目录 `catalogDigest`；配置修改使用 `expectedRevision`，旧草稿返回 409。 |
| 跨节点硬占额 | 在 PostgreSQL 中锁定应用账户，原子检查预算、分钟请求数/Token 上界并创建唯一请求预留。不同网关进程共享约束。 |
| 安全结算 | 用终态 Runtime 回执关联预留；成功消耗保守报价，确定未执行才释放，执行后未知保留占额。重复回执不重复扣除。 |
| 审计 | 配置、身份、凭证与调用变化同事务写入工作区哈希链；数据库触发器禁止普通事件更新/删除。导出包含序号、前序哈希、链头和校验结果。 |
| 节点观察 | 已认证节点回报状态；超过 90 秒未报告显示 stale。配置了节点认证不等于节点健康或真实模型调用成功。 |
| 缓存 | 默认关闭；AES-256-GCM 加密已检查文本，按租户/应用/主体/路线/策略/输入隔离，TTL、容量、失效修订和密钥版本约束。命中再次授权和输出检查，消耗执行 nonce 并独立审计，不占模型额度。 |
| 自动切换 | 管理员配置最多 3 个独立 catalog 模型作为备用；每次尝试单独核对数据类别、地域、身份和额度。仅在明确可重试 HTTP 结果且尚未释放输出时切换，不重试副作用工具或安全拒绝。 |
| 共享熔断 | PostgreSQL 中记录路线失败与熔断，跨节点按批准顺序跳过故障路线；安全输出拒绝不会被视为可绕过的供应商故障。 |
| 多模态检查 | 实际图片像素/PCM 音频归一化、哈希绑定、OCR/ASR 文字进入已有数据规则；检查失败拒绝，空白或低置信度另需未分类媒体授权。媒体不进入响应缓存。 |
| 秘密引用 | 提供方目的端与密钥采用 `env:SCARLET_GATEWAY_*` 引用；身份客户端秘密采用 `env:SCARLET_IDP_*` 引用。管理配置不包含实际秘密值。 |

预算单位是 USD 微美元。`budgetMicros` 是应用累计额度，不自动按月清零；0 模型预算拒绝模型调用，0 RPM 或 0 TPM 拒绝所有调用。当前按受支持输入字节数与最大输出 Token 计算保守报价，记录为占额/报价消耗，**不是上游供应商的实际账单**。现有规范化输出不提供完整供应商用量证据，不能据此伪造实际成本。工具与 RAG 不产生模型报价，仍受身份、调用限额、审批和审计约束。

跨节点请求 ID 在工作区内唯一。网络失败后不能把查到的旧授权当成一次新的执行许可。未决预留不会通过超时任务自动退款。数据库故障、授权接口不可用、目录或固定目的端不一致时拒绝执行。每次授权同时绑定治理配置修订；调用中撤销凭证、模型或工具授权，输出前重新校验后抑制结果。输入泄露或缺少独立批准等未进入预留的拒绝，也以不影响账务的独立元数据事件进入审计；未知版本只保留为未验证拒绝证据。

缓存终态与请求 nonce 在同一个数据库事务中计入 RPM/TPM；零请求或令牌限额同样拒绝缓存放行。零模型预算可使用仍满足授权和速率约束的零供应商成本缓存。成功 `completed` 回执表示上游结果已完成检查并记录占额结算，不证明客户端已经完整接收；流式断连或撤权可能在回放中止。未报告 `outputReleased` 也不能当作独立的客户端交付证明。

缓存密钥由控制面部署环境提供：`SCARLET_GATEWAY_CACHE_KEY` 为标准 Base64 编码的 32 个随机字节，对应应用策略 `keyRef=env:SCARLET_GATEWAY_CACHE_KEY`。不把实际密钥写入目录、数据库或审计。更换密钥时同时更新不可变 `keyVersion` 并失效旧缓存；数据库恢复仍需相应密钥。部署时需提供独立私有密钥，应用缓存默认关闭。

## 工作负载 JWT 与短期凭据

在现有网关应用区管理工作负载绑定。Owner 选择已保存且启用的应用，导入签发者的公开 JWKS，填写精确 `issuer`、`audience`、`subject` 与 1—300 秒的 `maxTtlSeconds`。名称最多 128 字节，issuer/audience 最多 512 字节，subject 最多 255 字节。issuer 为不含用户凭证、查询或片段的 HTTPS URL；不会由此发起发现请求。audience 可使用专用 URL 或 URN，宜为工作区单独分配。

| 接口 | 权限与结果 |
| --- | --- |
| `GET .../ai-gateway/workloads?before=...&limit=50` | 工作区成员读取绑定历史，返回 `items/hasMore/nextCursor`，默认 100 条、最大 200 条。 |
| `POST .../ai-gateway/workloads` | Owner 创建，字段为 `name/applicationId/issuer/audience/subject/jwks/maxTtlSeconds`，默认启用。 |
| `PUT .../ai-gateway/workloads/{id}` | Owner 提交完整字段、显式 `enabled` 与 `expectedRevision`；409 后重新读取并审核差异。 |
| `POST .../ai-gateway/workloads/{id}/revoke` | Owner 提交 `expectedRevision`，撤销绑定及其已兑换凭据。 |
| `POST .../ai-gateway/workload-token` | 独立机器认证，用签名 JWT 换取一次展示的 `credential/token/expiresAt`。 |

`jwks` 是包含 1—8 个公钥的 JSON 对象，最多 16 KiB。每个 JWK 必须有唯一 `kid`、`use:"sig"` 和明确 `alg`；`key_ops` 可省略，提供时只允许 `["verify"]`。支持 RS256（RSA 2048—4096 位、e=65537）、ES256（P-256）和 EdDSA（Ed25519）。公钥标识最多 80 个 ASCII 标识字符。私钥、对称密钥、证书/远程 key 地址字段及未知算法拒绝导入。私钥保留在签发端。

机器交换格式：

```http
POST /api/v1/workspaces/<workspace-slug>/ai-gateway/workload-token
Authorization: Bearer <SIGNED_WORKLOAD_JWT>
Content-Type: application/json

{"bindingId":17}
```

这里的 ID 是已创建绑定的真实 ID。交换接口不采用浏览器 Cookie；只为这个精确 POST 路径的独立 Bearer 请求提供 Origin 豁免。绑定管理接口仍要求平台会话和受信任 Origin。

JWT header 必须有与已导入公钥一致的 `alg/kid`，`typ` 为 `JWT` 或 `at+jwt`。只接受紧凑 JWS；header 最多 1 KiB、claims 最多 8 KiB、整个断言最多 12 KiB。动态 key 地址、嵌入公钥、未支持的关键扩展及重复/歧义字段拒绝。Claims 必须包含精确匹配的 `iss/sub/aud`、整数秒 `iat/exp` 和不超过 160 字节的 `jti`；aud 可为字符串或仅含一个精确受众的数组。签名有效期不超过 300 秒，`iat` 未来偏差最多 30 秒；过期立即拒绝。可选 `nbf` 另作生效时间检查。角色、应用、ACL 等额外声明不授予权限。

返回的 `sgw_` 凭据最长为绑定 TTL，且不长于 JWT 剩余有效期。保存、改密钥或撤销绑定都会在同一事务禁用该绑定已签发的全部凭据；新调用、缓存、任务及输出前复核采用当前状态。原有静态凭据保持各自的管理边界。任务绑定具体 `credentialId`，新兑换不会自动迁移旧任务；凭据到期后，相关任务及祖先授权停止，当前不提供无缝长任务续租。

`jti` 在工作区与签发者范围只兑换一次，数据库原子防重放；并发重复请求只有一次成功，其余为 409。保存了凭据但丢失交换响应时，应重新签发带新 `jti` 的断言，旧明文不能重取。401 表示签名、声明或绑定不可采用；403 表示当前应用授权不可采用。每次合格交换最多清理 500 条到期重放记录，活跃重放记录和凭据另有容量上限。原始 JWT、明文 token 和私钥不写入审计；审计记录绑定修订、签发者、受众、主体、公钥摘要、key ID、jti 摘要和到期时间。

此路径验证签发者的短期身份声明；模型、资料和工具授权仍由现有目录与任务决定。令牌是 Bearer 凭据，未实现 DPoP 或远程工作负载执行环境证明。

## OIDC 与 SCIM

企业登录采用 Authorization Code + PKCE S256、数据库一次性 state、浏览器绑定 Cookie、nonce，以及成熟 OIDC 库的签名/issuer/audience/有效期验证。仅接受已经 SCIM 开通的 issuer-scoped subject。邮箱、名称和令牌中的角色声明不用于链接本地账号或授予权限。

部署配置：

```text
SCARLET_OIDC_ALLOWED_ISSUERS=https://实际身份服务/实际租户
SCARLET_IDP_CLIENT_SECRET=<通过部署环境提供，禁止提交仓库>
```

在身份页面填写 `clientSecretRef=env:SCARLET_IDP_CLIENT_SECRET`，复制实际部署域名下的固定 callback 给 IdP。OIDC 的 issuer、发现的认证/令牌/JWKS 请求和应用 callback 使用 HTTPS。`SCARLET_OIDC_ALLOWED_ISSUERS` 按逗号分隔精确匹配，不接受前缀或任意子域。

| 接口 | 用途 |
| --- | --- |
| `GET /api/v1/identity/{slug}/oidc/start` | 发起企业登录 |
| `GET /api/v1/identity/{slug}/oidc/callback` | 消费一次登录状态并建立平台会话 |
| `GET/PUT .../ai-gateway/identity` | Owner 读取/按版本保存身份配置 |
| `POST .../ai-gateway/identity/scim-token` | 生成或轮换一次性展示的 SCIM 令牌，旧令牌立即失效 |
| `GET .../ai-gateway/identity/users` | Owner 按 `q/active/group` 过滤及 `cursor/limit` 分页核对账号；返回总数、匹配数和下一页信息。显式页长为 1—200。 |
| `/scim/v2/{slug}/Users`、`/Groups` | 工作区 Bearer 认证的 SCIM 开通、更新、停用与组维护 |

SCIM 实现 Users/Groups 的 GET、分页、POST、PUT、DELETE，以及受支持属性的 PATCH；支持精确 `id`、`externalId`、`userName` 和组 `displayName` 查询。用户 PATCH 支持 active/userName/displayName，组 PATCH 支持 displayName 和成员增删替换。未实现 Bulk、密码同步、排序或任意过滤表达式，不宣称完整 SCIM 协议认证。超出范围返回错误，不忽略。

SCIM `externalId` 必须映射为此应用 OIDC 的 `sub`。默认角色为 viewer；组 externalId 可以映射 viewer/reviewer/operator，不能映射 Owner。停用、角色变化和身份配置变更撤销相关会话。联邦账号已有记录时禁止直接更换 issuer/client ID，以免重新绑定主体。单工作区当前最多 10,000 个联邦账号、1,000 个组，每组最多 1,000 个成员。

关闭 `enabled` 撤销联邦登录和成员访问；`scimEnabled` 只控制机器同步入口。SCIM 令牌与 OIDC 客户端密钥不同，不能互相代替。管理员保存配置成功不代表真实 IdP 已完成端到端联调。

## 网关节点认证

控制面环境采用工作区绑定：

```text
SCARLET_GATEWAY_WORKSPACE=<工作区 slug>
SCARLET_GATEWAY_CONTROL_TOKEN=<随机节点认证值，32 至 512 字节>
```

多工作区部署可使用 `SCARLET_GATEWAY_CONTROL_TOKENS` 的 `{workspaceSlug: token}` JSON。没有不分工作区的全租户默认令牌。节点通过固定 HTTPS 控制面调用 `/api/v1/internal/ai-gateway/resolve`、`authorize`、`settle`、`heartbeat`。`resolve` 发送客户端凭证哈希，不能发送客户端明文密钥或让调用方自报可信身份。节点令牌属于工作区可信执行边界，应通过私有挂载文件/环境管理。

## 运维边界

Agent 任务绑定工作区内凭证，约束精确工具动作/目标、收缩委托、祖先步数与预算、递归停止及在途取消；知识资料经实际读取和正文核验后进入模型，来源与敏感类别跨步骤继续施加用途、留存和训练限制。任务与授权范围见[任务控制](ai-gateway-tasks.md)，真实私网授权源接口及目的端约定见[来源连接器](ai-security-source-connector.md)。该能力适用于实际接入的支持路径，不是对任意 Agent 或远端供应商行为的通用证明。

模型、RAG、缓存、备用路线、SSE 和 MCP 采用当前任务控制；控制面要求任务时，缺少任务会在资料/媒体工作前拒绝。成功来源和类别由真实运行回执贯通；任务声明本身不赋予直接读取历史资料的权限。模型报价使用实际构建的外发字节，包含实际来源和媒体归一化，来源摘要随占额及结算核对。任务控制需先采用 060 迁移；来源轮换与工作负载身份另需 061、063 迁移及相应控制面。启用 `scarlet-detectors-v1` 前升级实际运行节点，重新发布匹配目录。旧健康回报不能代替新能力验收。

持续审计导出可运行：

```sh
scarlet-gateway-audit-export \
  -platform https://实际控制面域名 \
  -workspace 实际工作区slug \
  -output /private-audit/gateway.jsonl \
  -follow
```

令牌从 `SCARLET_GATEWAY_CONTROL_TOKEN` 读取，也可以指定 `-token-env`。导出器校验每页序号、工作区、前序哈希和链头，私有 JSONL 写入并 fsync 后才更新 checkpoint；服务端临时故障不推进游标，重启从已验证位置恢复。配置、文件权限及恢复行为见[导出器说明](../backend/cmd/gateway-audit-export/README.md)。JSONL 可交给既有日志采集器转发，不包含模型输入输出正文。

多网关节点共享数据库占额、审计序列和一次授权约束，不依赖 Bifrost Enterprise 的内存状态同步。PostgreSQL 自身的高可用、备份、复制和故障切换仍由部署环境提供；本地多连接池并发测试不等于完成生产容灾演练。安全目录由受控文件发布，控制面通过目录哈希拒绝未同步节点；当前不自动向客户节点推送或热改固定上游配置。

审计是应用层防篡改证据链，普通 SQL 更新/删除由触发器阻止；数据库超级管理员仍在信任边界内。工作区整体删除是保留边界。需要独立留存时，将已校验的导出写入独立受限存储/现有日志采集系统。当前不宣称已经接通客户 S3/GCS、外部 WORM 或取得合规认证。

跨供应商切换采用明确批准的顺序与共享熔断，不做根据价格或模型自行判断的隐式换路。429/502/503/504 可以在未释放输出时进入下一条授权路线；未知网络结果不会自动重发，未知费用保留原占额。缓存与 provider admission 共享跨节点请求唯一性，不能通过改节点重复执行。企业身份、集群和检测模型的客户生产参数不能由本地模拟测试替代。客户 PostgreSQL 高可用、外部 WORM、真实知识库权限连接器及全协议/全媒体兼容仍按各自验收范围推进，不宣称已经具备。
