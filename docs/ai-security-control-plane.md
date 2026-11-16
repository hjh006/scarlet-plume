# AI 安全控制面

本模块持久化工作区 AI 目录、策略修订、控制台判定、当前配置评测和独立审批。基础迁移为 `050_ai_security.sql`，版本化测试包为 `053_ai_security_regression_packs.sql`。策略引擎复用 `backend/pkg/aisecurity`；本模块不复制检测引擎，也不执行模型、RAG 或工具调用。

## 已实现契约

API 前缀：`/api/v1/workspaces/{slug}/ai-security`。使用现有会话和同源校验；所有查询按 workspace_id 隔离。

| 路径 | 输入/输出与权限 |
|---|---|
| GET `/` | bundle：configuration、revision、updatedBy/At、publishedRevision、publishedDigest、publishedAt，以及 decisions、evaluations、approvals、coverage；所有成员可读 |
| PUT `/configuration` | `{expectedRevision,configuration}`；仅 owner/operator；初始修订 0，CAS 成功递增；拒绝客户端写入任何 approvals |
| POST `/evaluate` | 引擎 Request；仅 owner/operator；返回 `{id,mode:"evaluation",configurationRevision,createdAt,decision}`；仅持久化不含正文的 Decision |
| POST `/evaluations` | `{packId}`；仅 owner/operator；冻结当前配置、测试包与引擎版本，保存 cases/status/coverageGaps/gateEligible；`{}` 仅为 legacy 诊断，不能新发布 |
| POST `/publish` | `{expectedRevision,evaluationId,note}`；独立 owner/reviewer；需要同修订、同摘要、当前引擎/检测版本、包族最新版本及完整必测通过；发布前重新运行；理由、包摘要、引擎与重新核对时点追加到 publicationReviews |
| POST `/approvals` | `{expectedRevision,request}`；owner/operator；仅引擎确认进入高影响工具审批门槛的请求；正文 input/output 必须为空；有效期最多15分钟，并同时受策略请求时限约束 |
| POST `/approvals/{id}/review` | `{decision:"approved"\|"rejected",note}`；owner/reviewer，且不能是申请人或当前配置作者；版本、摘要、阶段及到期时间再核对 |
| GET `/export` | 同 bundle 有界预览范围的 JSON 证据导出；明确 mode=evaluation；不是全量历史导出 |
| GET `/runtime-config` | 仅下载独立发布的配置；未发布返回409；附加该发布修订、摘要下未过期的真实批准；不包含运行时认证秘密或上游凭据 |

configuration 的 Config、Application、Principal、Document、Model、Tool、Policy 字段以 `pkg/aisecurity/types.go` 为准。EndpointRef 是客户侧端点映射引用，不是直接上游 URL 或密码字段。HTTP JSON 使用 DisallowUnknownFields，`token` 等未知字段不会作为配置接收。资料/ACL/分类来自服务端目录，不能在 Request 中自报为可信字段。

## 范围与可信性

- 服务端目录是授权人员维护的配置，不自动成为独立源端观察或已验证的 ACL；coverage.trustedSources 固定0，实际运行来源使用独立 runtime-evidence API。
- 控制台 Request 的 applicationId、principalId 是评估声明。真实客户运行时必须由认证上下文绑定身份，不能据此把控制台 allow 解释为真实执行。
- 原始 input/output 和完整模型请求体不入库；决策存摘要、标识、规则及覆盖说明。高影响审批保存受目录约束的工具参数以供独立核对，不保存正文。
- ApprovalID 为审批记录 ID 的十进制字符串。批准绑定 RequestDigest（包括请求ID、时间、参数等）及 ConfigDigest；更改请求会失配，更改配置会将旧 proposed/approved 记录标记 invalidated。
- 待审列表最多100条；明确审批ID的复核不依赖列表是否仍在预览范围。执行授权另按 revision/digest/status/expiry 查询，最多1000条，超额显式拒绝，不能被新的预览记录静默挤出。
- 新草稿不覆盖已发布配置。运行模板保持已发布修订；草稿修改会使既有动作批准失效，不能保留旧高影响批准穿越变更。

## 评测发布门禁

评测使用客户当前 Config 和真正的 Evaluate。版本化基线还明确标记派生目录契约：在内存撤销授权或隔离约束以验证特定边界，不部署或冒充当前源端事实。

每个已启用的资产族，构造一个目录内正向或高影响工具的无批准拒绝门槛用例，并执行未知主体、未知目标、过期请求的否定用例。模型请求按当前支持协议构造受限 JSON，明确模型和 messages，不实际调用供应商。RAG 候选先按真实目录 ACL 筛选；兼容1..59秒策略时限。评测最多8192个候选，若耗尽则明确预算截断并保持 failed，不谎称已穷尽。

显式停用模式要求至少一个已登记应用且所有应用 disabled；实际执行拒绝用例，scope 标为“仅拒绝路径验证，不表示业务可用”。空初始目录仍 failed。启用配置因缺少可用身份、ACL或目标而无法构造正例时不能冒充停用通过。

门禁仅证明所列引擎契约在冻结修订上的结果；不证明客户端接入完成、全部业务用例、安全攻击覆盖、完整来源ACL、供应链可信、真实模型调用或独立复测完成。

22 探针、测试包校验/导入/读取接口、最新包失效、定义变化比较及升级门禁细则见[版本化回归契约](ai-security-regression.md)。旧评测、已发布配置与来源证据不被新测试结果回填。

## 验证

`PLUME_STORE_TEST_DSN` 指向一次性 PostgreSQL 时：

```sh
cd backend
go test ./internal/store ./internal/httpapi -run '^Test(AISecurity|AIRegression|AIRuntime)' -count=1
```

覆盖真实数据库持久化/CAS、角色与跨工作区反例、自发布/自审批拒绝、冻结修订评测、短TTL、513文档末尾可用ACL、明确停用、审批被100条预览挤出后的复核和使用、参数变化失效、草稿修订失效、HTTP同源/未知字段、运行模板与无正文导出。
