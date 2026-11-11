# 逐页设计规格与功能映射

范围：53个桌面页面与状态，19个现有一级模块；手机端延期。以下以当前源文件和路由为映射，ImageGen提示词是视觉说明，不是增删接口或枚举的依据。

## 01 数据安全全景

- 模块：数据治理
- 入口：`overview`
- 现有组件：`WorkspaceOverview.vue`
- 预览：[打开](preview.html?screen=1)；截图：[PNG](renders/01-overview.png)
- 视觉基准：ImageGen统一视觉系统；逐页预览为完整内容与桌面构图依据。

对应已有选定ImageGen主稿；以同一设计系统完整还原。

## 02 数据资产台账

- 模块：数据治理
- 入口：`assets`
- 现有组件：`WorkspaceView.vue`
- 预览：[打开](preview.html?screen=2)；截图：[PNG](renders/02-assets.png)
- 视觉基准：ImageGen统一视觉系统；逐页预览为完整内容与桌面构图依据。

对应已有选定ImageGen主稿；以同一设计系统完整还原。

## 03 检查与独立复核

- 模块：安全运营
- 入口：`checks/:id`
- 现有组件：`CheckDetail.vue`
- 预览：[打开](preview.html?screen=3)；截图：[PNG](renders/03-check-detail.png)
- 视觉基准：ImageGen统一视觉系统；逐页预览为完整内容与桌面构图依据。

对应已有选定ImageGen主稿；以同一设计系统完整还原。

## 04 业务与数据资产目录

- 模块：数据治理
- 入口：`asset-catalog`
- 现有组件：`AssetCatalogView.vue`
- 预览：[打开](preview.html?screen=4)；截图：[PNG](renders/04-catalog.png)
- 视觉基准：ImageGen统一视觉系统；逐页预览为完整内容与桌面构图依据。

Select 业务目录 in the sidebar. Heading 业务与数据资产目录. Master-detail explorer: left expandable tree 业务 / 应用 / 数据库 / 表 / 字段, center dense catalog table columns 名称、类型、责任团队、来源、当前状态; right selected customer-service metadata panel with 来源观察、人工确认、冲突 tabs. Explain automatic observations and confirmed business attributes separately. Actions 导入 CMDB、确认属性、查看历史. A restrained feather branch breadcrumb connects business→application→database→table without inventing lineage. Show a visible conflict badge for 保留期限, no automatic override.

## 05 分类分级台账

- 模块：数据治理
- 入口：`classification`
- 现有组件：`ClassificationView.vue`
- 预览：[打开](preview.html?screen=5)；截图：[PNG](renders/05-classification.png)
- 视觉基准：ImageGen统一视觉系统；逐页预览为完整内容与桌面构图依据。

Select 分类分级. Heading 分类分级. Compact strip 资产总数、待确认、人工确认、条件缺口. Filtered table columns 资产、数据属性、业务级别、法定类别、采用标准、判定来源、操作. Use values 个人信息候选、内部、未确认、自动识别/人工确认. Important/core statutory category remains 未核实 unless evidence supplied. Actions 选择参照标准、查看判定依据、确认分类. Narrow right explanation panel 采用版本与缺失条件, no risk scoring donut or fake compliance percentage.

## 06 分类分级标准库

- 模块：数据治理
- 入口：`classification/standards`
- 现有组件：`ClassificationStandardsView.vue`
- 预览：[打开](preview.html?screen=6)；截图：[PNG](renders/06-standards.png)
- 视觉基准：ImageGen统一视觉系统；逐页预览为完整内容与桌面构图依据。

Select 分类分级. Heading 分类分级标准库. Left industry filter 全部/通用/金融/电信/医疗/工业; a searchable standards catalog as precise table rows showing 标准名称、编号、采用版本、规则覆盖、全文状态. Right default-standard selection panel 工作区默认标准 with choose/save and current version. Show badges 已核对条款、目录待补齐、参考规则, distinguish enforceable law versus recommended standard. Bottom selected-standard detail has 适用范围 / 规则 / 公开来源 tabs and active version. Do not claim all industry standards fully implemented or certified.

## 07 分类确认与自动判定依据

- 模块：数据治理
- 入口：`classification/:asset`
- 现有组件：`ClassificationView.vue ClassificationEvaluation.vue`
- 预览：[打开](preview.html?screen=7)；截图：[PNG](renders/07-classification-detail.png)
- 视觉基准：ImageGen统一视觉系统；逐页预览为完整内容与桌面构图依据。

Heading 分类确认与判定依据. Asset inventory.classification_contacts. Left main adopted-standard/version block, rule hit evidence table 命中规则、自动条件、统计、证据批次 and explicit missing conditions. Right editable confirmation form 数据属性、业务级别、法定类别、分类依据 with statutory 未核实 retained. Separate candidate source result from manual confirmation. Bottom version history with before/after comparison. Actions 保存确认、查看原始批次、返回台账. Use muted violet for candidate, teal for confirmed, coral only conflict.

## 08 数据流转图谱

- 模块：数据治理
- 入口：`data-flows`
- 现有组件：`DataFlowsView.vue`
- 预览：[打开](preview.html?screen=8)；截图：[PNG](renders/08-data-flows.png)
- 视觉基准：ImageGen统一视觉系统；逐页预览为完整内容与桌面构图依据。

Select 数据流转. Heading 数据流转. Main large midnight canvas with feather-shaped graph built from genuine typed paths: databases → application/API → field categories → evidence; six main shaft nodes and branching paths. Controls 服务、来源、数据类别、焦点 and 当前图谱/历史快照/调查. Right evidence rail has selected relationship source, trace/span proof, time window, explicit 观察关系 versus 已确认关系 and unknown gaps. Exact main labels PostgreSQL、客户服务、订单 API、邮箱字段、对外共享、检查证据. No invented edges solely to complete a feather. Zoom/focus/legend and clear source/time scope.

## 09 图谱历史与版本差异

- 模块：数据治理
- 入口：`data-flows:history`
- 现有组件：`DataFlowsView.vue`
- 预览：[打开](preview.html?screen=9)；截图：[PNG](renders/09-graph-history.png)
- 视觉基准：ImageGen统一视觉系统；逐页预览为完整内容与桌面构图依据。

Select 数据流转. Heading 图谱历史与差异. Comparison controls 快照 A / 快照 B with dates and source scope; two synchronized miniature dark feather relationship canvases side-by-side, visible selected node. Below a dense difference table 关系、新增/未再观察/标签变化、来源、证据. Use teal added, muted dashed unobserved, violet label changes, not red deletion. Right small frozen-version summary and action 导出差异. State 未再观察不等于源资产已删除.

## 10 请求与数据关系调查

- 模块：数据治理
- 入口：`data-flows:investigation`
- 现有组件：`DataFlowsView.vue`
- 预览：[打开](preview.html?screen=10)；截图：[PNG](renders/10-investigation.png)
- 视觉基准：ImageGen统一视觉系统；逐页预览为完整内容与桌面构图依据。

Heading 请求与数据关系调查. Trace and source filters. A clean vertical investigation spine with connected cards 应用入口 → API 请求 → 数据库目标 → 字段候选 → 检查证据; labels service-call proof / parent span rather than raw payload. Right selected trace evidence rail with matched source aliases, exact database/schema/table, observed time and limitations. Lower table source-declared pseudonymous subject references scoped by sensor, NOT real people counts. Controls 聚焦关联节点、查看来源证明、导出调查. No raw personal values, request bodies, passwords or SQL.

## 11 处理活动台账

- 模块：数据治理
- 入口：`activities`
- 现有组件：`ActivitiesView.vue`
- 预览：[打开](preview.html?screen=11)；截图：[PNG](renders/11-activities.png)
- 视觉基准：ImageGen统一视觉系统；逐页预览为完整内容与桌面构图依据。

Heading 处理活动. Compact filter tabs 全部 / 内部处理 / 对外共享 / 数据出境 / AI 使用. Dense table 活动名称、关联资产、场景、接收方/区域、责任团队、最近变更、评估状态. A violet AI row and coral overdue assessment row. Primary 新建活动. Right small selected-activity summary with current revision, purpose, sharing target, related checks/assessments. Reassessment status shows 已变化待重评, not automatic approved.

## 12 新建与编辑处理活动

- 模块：数据治理
- 入口：`activities/new + activities/:id:edit`
- 现有组件：`ActivityCreate.vue ActivityDetail.vue`
- 预览：[打开](preview.html?screen=12)；截图：[PNG](renders/12-activity-form.png)
- 视觉基准：ImageGen统一视觉系统；逐页预览为完整内容与桌面构图依据。

Heading 新建处理活动. Single primary form with feather-spine section navigation 关联资产 → 处理目的 → 接收与区域 → 保留与保障. Visible fields 活动名称、关联资产、场景、处理目的、接收方、国家/地区、保留期限、传输路径、保障措施. Select AI 使用 as an example and show only relevant helpful hints, no implementation jargon. Bottom sticky 保存活动 / 取消. Right context panel current asset, source facts, missing business evidence. Editing state carries 当前修订 v2 and clear version-conflict retained-draft message as a small inset state.

## 13 处理活动详情与变更历史

- 模块：数据治理
- 入口：`activities/:id`
- 现有组件：`ActivityDetail.vue`
- 预览：[打开](preview.html?screen=13)；截图：[PNG](renders/13-activity-detail.png)
- 视觉基准：ImageGen统一视觉系统；逐页预览为完整内容与桌面构图依据。

Heading 合成客户服务处理活动 with scene tag 对外共享 and revision v2. Header 编辑活动 / 导出历史. Top definition layout purpose, asset, recipient/region, retention/path/safeguards. Middle timeline feather spine v1→v2 with exact before-after purpose comparison. Right associated checks/PIA/cross-border/AI assessments showing stale/reassessment badges. Lower history audit table 版本、变更字段、操作者、时间, current facts separate from frozen assessment.

## 14 安全运营与处置概览

- 模块：安全运营
- 入口：`operations`
- 现有组件：`SecurityOperationsView.vue`
- 预览：[打开](preview.html?screen=14)；截图：[PNG](renders/14-operations.png)
- 视觉基准：ImageGen统一视觉系统；逐页预览为完整内容与桌面构图依据。

Select 运营与处置. Heading 安全运营与处置. Tabs 运营概览 / 安全事件 / 处置动作 / 集成来源. Compact metrics 近30天事件、未闭环事件、待独立审批、逾期风险; understated two-color event/review trend, responsibility-team table, prioritized queue. A small horizontal feather workflow from 来源信号→检查→整改→复测→复核, connected to tasks. Source counts explicitly 配置来源, not online ratio. No decorative full graph dominating this operational overview.

## 15 安全事件台账

- 模块：安全运营
- 入口：`operations:events`
- 现有组件：`SecurityOperationsView.vue`
- 预览：[打开](preview.html?screen=15)；截图：[PNG](renders/15-security-events.png)
- 视觉基准：ImageGen统一视觉系统；逐页预览为完整内容与桌面构图依据。

Heading 安全事件. Tabs as operations with 安全事件 selected. Table 事件/规则、来源、范围、首次/最近观测、关联检查、当前阶段、操作, filters 来源/严重级别/阶段. Selected Wazuh rule 公共导出配置 has source outcome 来源报告发现, not confirmed data breach. Right event panel shows source-generated observation count and later 明确复测消除, same canonical rule and scope, original raw logs stay source-side. Visible source id and evidence link, no hand-add-event button.

## 16 处置动作与执行回执

- 模块：安全运营
- 入口：`operations:controls`
- 现有组件：`SecurityOperationsView.vue`
- 预览：[打开](preview.html?screen=16)；截图：[PNG](renders/16-controls.png)
- 视觉基准：ImageGen统一视觉系统；逐页预览为完整内容与桌面构图依据。

Heading 处置动作. Table 动作、限定目标、申请人、独立审批、执行状态、有效期、操作. Selected PostgreSQL 撤销表级 SELECT action shows expandable right detail with spec, approval, durable execution receipt before true/after false, and timeline. Tabs 申请范围 / 审批 / 执行 / 回滚. Clearly label 来源回执执行成功, not risk verified; link to source retest check. Another row API临时阻断 has expiry semantics. Action 申请回滚 requires reason.

## 17 处置申请与独立审批

- 模块：安全运营
- 入口：`operations:request/approve`
- 现有组件：`SecurityOperationsView.vue`
- 预览：[打开](preview.html?screen=17)；截图：[PNG](renders/17-control-form.png)
- 视觉基准：ImageGen统一视觉系统；逐页预览为完整内容与桌面构图依据。

Heading 申请受控处置. Stepped form 选择动作 → 限定目标 → 审批范围. Fields 处置类型 撤销表级SELECT, 执行来源, 范围内数据库表, 已观察角色 PUBLIC, 关联检查, 执行授权窗口, 处置原因. All target identifiers chosen from actual observed inventory, no arbitrary SQL input. Right approval summary shows original grant and independent reviewer requirement. Footer 提交审批 / 取消. Small separate inset illustrates reviewer read-only approval view with 同意执行 button and revision scope, applicant cannot approve. PG execution window expiry never auto-restores privileges.

## 18 事件与执行来源配置

- 模块：配置接入
- 入口：`operations:sources`
- 现有组件：`SecurityOperationsView.vue`
- 预览：[打开](preview.html?screen=18)；截图：[PNG](renders/18-operation-sources.png)
- 视觉基准：ImageGen统一视觉系统；逐页预览为完整内容与桌面构图依据。

Heading 集成来源. Source table Wazuh事件来源 / PostgreSQL权限执行器 with purpose, bound scope, last received, revoked state. Right source creation form 名称、用途、责任团队、绑定已有PG采集任务 (when control). A small token-success state panel uses only masked placeholder dots, label 仅显示一次, actions 复制并安全保存 / 关闭; NO actual token. A separate compact revoke confirmation state warns active physical action needs rollback before source revocation. Show protocol details as optional collapsed help.

## 19 API契约安全与扫描

- 模块：API安全
- 入口：`api-security`
- 现有组件：`ApiSecurityView.vue`
- 预览：[打开](preview.html?screen=19)；截图：[PNG](renders/19-api-security.png)
- 视觉基准：ImageGen统一视觉系统；逐页预览为完整内容与桌面构图依据。

Select API安全. Heading API 安全. Top compact risk strip and tabs 契约扫描 / 运行时观测 / 接口生命周期. Main recent-scan table 服务、契约版本、接口数、待核查发现、采集时间. Right 新建契约扫描 form source alias, OpenAPI JSON file upload, scope and 扫描 button, empty preview rectangle with source-only file text, no real credentials. Below a few prioritized findings tied to checks. Claims are contract signals only, not proof of exploitable vulnerability.

## 20 API契约扫描详情

- 模块：API安全
- 入口：`api-security/:scan`
- 现有组件：`ApiSecurityView.vue`
- 预览：[打开](preview.html?screen=20)；截图：[PNG](renders/20-api-scan-detail.png)
- 视觉基准：ImageGen统一视觉系统；逐页预览为完整内容与桌面构图依据。

Heading 客户服务 · 契约扫描详情. Adopted version/time/source summary and 接口 / 发现 / 字段 tabs. Table method/path, authentication declaration, sensitive field candidates, deprecated marker, linked check. Selected GET /api/orders/{id} detail shows request/response field tree, missing-auth contract warning, adoption basis. Diff link to prior version. Badges 契约声明 / 待运行时验证, never assert current authentication from header presence. Right 查看关联检查 and 导出JSON.

## 21 运行时观测与来源

- 模块：API安全
- 入口：`api-security/runtime`
- 现有组件：`ApiRuntimeView.vue`
- 预览：[打开](preview.html?screen=21)；截图：[PNG](renders/21-runtime.png)
- 视觉基准：ImageGen统一视觉系统；逐页预览为完整内容与桌面构图依据。

Heading 运行时观测. Tabs 路由观测 / 来源 / 检测发现. Compact requests/status/source metrics with per-source deduplication note. Dense route table 来源、服务、方法、路由、请求数、响应、最新观测. Right selected-source summary gateway/OTLP, heartbeat, inspection coverage, source-side privacy. Controls 新增观测来源 / 立即刷新 / 查看请求审计. Show connection metadata and no raw URL params or sensitive headers.

## 22 API流量监测与请求审计

- 模块：API安全
- 入口：`api-security/runtime:monitor`
- 现有组件：`RuntimeMonitor.vue AuditView.vue`
- 预览：[打开](preview.html?screen=22)；截图：[PNG](renders/22-runtime-monitor.png)
- 视觉基准：ImageGen统一视觉系统；逐页预览为完整内容与桌面构图依据。

Heading API 流量监测. Filters 服务、来源、方法、状态、时间范围. Charts are restrained requests line and HTTP status bars; lower request audit list with trace/span pseudonymous references, canonical routes, status and category stats. Right selected request proof table data-field types/counts and inspection locations, coverage gap labels unknown / partial. Do not sum gateway and OTLP as distinct user requests. Exact action 查看调查链路 and 导出审计.

## 23 运行时风险研判与分派

- 模块：API安全
- 入口：`api-security/runtime:findings`
- 现有组件：`RuntimeFindingsPanel.vue RuntimeRiskEditor.vue RuntimeDetectionFacts.vue`
- 预览：[打开](preview.html?screen=23)；截图：[PNG](renders/23-runtime-risk.png)
- 视觉基准：ImageGen统一视觉系统；逐页预览为完整内容与桌面构图依据。

Heading 运行时风险研判. Selected rule 敏感响应信号 with adopted policy v2, original observation source/window/count/denominator and explicit 不等于已发生泄露. Main evidence and related-check sections; right triage form 风险等级、责任团队、整改期限、研判原因 with revision. Bottom historical triage and policy snapshot comparison, immutable old detection versus new policy separate. Buttons 保存研判 / 查看检查 / 查看源复测.

## 24 API主动验证任务

- 模块：API安全
- 入口：`validation`
- 现有组件：`ApiValidationView.vue`
- 预览：[打开](preview.html?screen=24)；截图：[PNG](renders/24-validation.png)
- 视觉基准：ImageGen统一视觉系统；逐页预览为完整内容与桌面构图依据。

Heading API 主动验证. Tasks table 名称、服务与路由、限定来源、规则、审批状态、最新结果、频率, controls 新建任务 / 来源筛选. Rows include 双账号对象授权验证 and 显式测试凭据验证, approved/draft/disabled states. Results separate 后台确认风险 / 本轮未观测 / 无法判定. Right task summary and safe scope illustration A自有、B自有、B访问A对象. Credentials stay source-side; no password fields on platform.

## 25 主动验证范围配置与审批

- 模块：API安全
- 入口：`validation:new/edit/approve`
- 现有组件：`ApiValidationView.vue`
- 预览：[打开](preview.html?screen=25)；截图：[PNG](renders/25-validation-form.png)
- 视觉基准：ImageGen统一视觉系统；逐页预览为完整内容与桌面构图依据。

Heading 新建主动验证任务. Form source, task name, service alias, GET method, route template, fixed target origin with no userinfo/path, manifest reference, selected rules BOLA/已提供测试凭据策略, frequency. Source-side secret reference notice not real secrets input. Right exact approved-scope summary and 三请求对照 diagram A→A、B→B、B→A. Save scope revision invalidates prior approval. Footer 保存待审批 and cancellation. Small reviewer state shows independent approval button and immutable scope, no dictionary brute force.

## 26 主动验证结果与判定依据

- 模块：API安全
- 入口：`validation/:id`
- 现有组件：`ApiValidationView.vue`
- 预览：[打开](preview.html?screen=26)；截图：[PNG](renders/26-validation-run.png)
- 视觉基准：ImageGen统一视觉系统；逐页预览为完整内容与桌面构图依据。

Heading 双账号对象授权验证 · 运行结果. Approved scope header and current adopted revision; tabs 运行历史 / 依据 / 检查. Main three-request comparison shows A自有200、B自有200、B交叉403, source-side object ownership confirmation, outcome 本轮未观测到风险, not generic passed. Previous run vulnerable and one inconclusive visible as compact timeline. Right gaps/projection state, linked check, JSON/HTML export. Connection-only run is clearly 连接诊断，无安全结论. Source result cannot auto-complete independent review.

## 27 API生命周期与版本变更

- 模块：API安全
- 入口：`api-lifecycle`
- 现有组件：`ApiLifecycleView.vue`
- 预览：[打开](preview.html?screen=27)；截图：[PNG](renders/27-lifecycle.png)
- 视觉基准：ImageGen统一视觉系统；逐页预览为完整内容与桌面构图依据。

Heading API 生命周期. Table unified canonical routes with 契约声明/运行时观测 provenance, 活跃/未登记/弃用/未再观察 status labels. Selected route opens right version diff showing old/new field declarations and source observations; no inference that not observed equals deleted. Filter exact service/source. Bottom recent changes strip has version A→B and source reference. Controlled violet change markers, coral only risk, no decorative generic lifecycle infinity loops.

## 28 运行时检测策略与版本

- 模块：API安全
- 入口：`runtime-policies`
- 现有组件：`RuntimePoliciesView.vue`
- 预览：[打开](preview.html?screen=28)；截图：[PNG](renders/28-policies.png)
- 视觉基准：ImageGen统一视觉系统；逐页预览为完整内容与桌面构图依据。

Heading 运行时检测策略. Left rule list nine rows 敏感响应、凭据字段、缺少认证、主体频次、错误突增、非工作时段、SQL模式、脚本模式、路径穿越; enabled/disabled/version pills. Right selected policy form 统计窗口、最少命中计数、最少请求数、错误比例/时区时段 as relevant. Show source-specific denominator example and explanatory gap. Bottom version history with adopted detections remaining immutable. Save button owner-only; viewer sees clearly read-only.

## 29 监管检查台账

- 模块：安全运营
- 入口：`checks`
- 现有组件：`ChecksView.vue`
- 预览：[打开](preview.html?screen=29)；截图：[PNG](renders/29-checks.png)
- 视觉基准：ImageGen统一视觉系统；逐页预览为完整内容与桌面构图依据。

Heading 监管检查. Compact status strip 草稿 / 整改中 / 待独立复核 / 已复核. Dense table 检查事项、条款、关联资产/活动、责任团队、证据状态、当前阶段、操作. Filters standard/template/scene/source/status, primary 新建检查. Selected row opens evidence summary rail with source freshness and next action. Important states 未取证、来源过期、待复测 distinct from failures. Feather motif as discreet process column rather than decorative illustration.

## 30 新建监管检查

- 模块：安全运营
- 入口：`checks/new`
- 现有组件：`CheckCreate.vue TemplateCriteria.vue`
- 预览：[打开](preview.html?screen=30)；截图：[PNG](renders/30-check-create.png)
- 视觉基准：ImageGen统一视觉系统；逐页预览为完整内容与桌面构图依据。

Heading 新建监管检查. Stepped left section navigation 关联范围 → 采用要求 → 责任与备注. Main form asset/activity, requirement or published template, specific template item, responsibility team, purpose note. A generous structured adopted-item panel has 适用条件 / 取证要求 / 验收标准 with exact labels. Right adopted template version and source scope summary. Footer 创建检查 / 取消. Missing applicability keeps unknown, never adds blanket compliance claim.

## 31 证据登记与来源取证

- 模块：安全运营
- 入口：`checks/:id:evidence`
- 现有组件：`CheckEvidenceForm.vue CheckDetail.vue`
- 预览：[打开](preview.html?screen=31)；截图：[PNG](renders/31-evidence.png)
- 视觉基准：ImageGen统一视觉系统；逐页预览为完整内容与桌面构图依据。

Heading 取证与整改记录. Main evidence form 类型、来源系统、来源记录引用、观测时间、摘要; no raw sensitive blob upload invented. Separate source acquisition cards PostgreSQL自动取证 / 外部协作者权限 with allowed target, freshness and run info. Right timeline preserves observation/remediation/retest separately. Manual evidence cannot replace source-bound retest. Include inline validation near time fields, saved feedback and read-only adopted snapshot. Footer 登记证据 / 取消.

## 32 专项合规评估台账

- 模块：安全运营
- 入口：`assessments`
- 现有组件：`AssessmentsView.vue`
- 预览：[打开](preview.html?screen=32)；截图：[PNG](renders/32-assessments.png)
- 视觉基准：ImageGen统一视觉系统；逐页预览为完整内容与桌面构图依据。

Heading 合规评估. Four scenario tabs PIA / 数据出境 / AI 使用 / 业务变更, task list evaluation title/activity/version/status/due/stale. Primary 创建专项评估. Right creation panel selects existing activity, published template, deadline, periodic refresh. Lower versioned template catalog rows with 草稿/已发布/已停用, owner actions. Unknown business facts and stale completed evaluations remain prominent. No percentage gauge replacing review.

## 33 专项评估答复与独立复核

- 模块：安全运营
- 入口：`assessments/:id`
- 现有组件：`AssessmentsView.vue`
- 预览：[打开](preview.html?screen=33)；截图：[PNG](renders/33-assessment-detail.png)
- 视觉基准：ImageGen统一视觉系统；逐页预览为完整内容与桌面构图依据。

Heading 客户服务 PIA 评估. Header status 已变化待重评, adopted version and current activity revision. Feather workflow 事实 → 答复 → 缺口 → 独立复核. Main split view question navigation and active answer form: single/multi/text/number/evidence types with source auto facts separate from business attestation. Show unknown 去重主体数量 and lawful-basis fact pending evidence, no auto-fill from sample rows. Right adopted evidence, outstanding linked risk and review panel. Footer 保存答复 / 提交复核 / 导出采用报告, historical frozen assessment visually distinct from current stale markers.

## 34 专项评估模板编辑

- 模块：安全运营
- 入口：`assessments:templates`
- 现有组件：`AssessmentsView.vue`
- 预览：[打开](preview.html?screen=34)；截图：[PNG](renders/34-assessment-template.png)
- 视觉基准：ImageGen统一视觉系统；逐页预览为完整内容与桌面构图依据。

Heading 评估模板草稿. Left sortable-style question outline (no unrequested drag requirement), center question editor with 标题、题型、选项、必填、适用条件、依据链接、义务性质、取证要求、验收标准、自动事实键. Right preview of question and immutable published revision note. Toolbar 保存草稿 / 发布新版本, separate 停用已发布版本. Distinguish 法定义务 and 治理建议; copying built-in template creates new draft, no edit in-place historical templates.

## 35 合规报告预览与导出

- 模块：报告审计
- 入口：`reports`
- 现有组件：`ComplianceReportView.vue`
- 预览：[打开](preview.html?screen=35)；截图：[PNG](renders/35-report.png)
- 视觉基准：ImageGen统一视觉系统；逐页预览为完整内容与桌面构图依据。

Heading 合规报告. Scope selector 全工作区 / 处理活动 and source freshness/time header. Main white document-like preview with contents rail: 范围与采用依据、来源与识别、检查结论、整改与复测、独立复核、专项评估引用、证据附录. Compact evidence adoption table source revision/rule/standard/version, current gaps, statuses. A very subtle feather-spine document navigation not giant background art. Right export controls HTML / JSON / 打印 and frozen versus current scope. Unknown and stale remain visible; no fabricated compliance score or certification stamp.

## 36 历史报告归档

- 模块：报告审计
- 入口：`reports/archives`
- 现有组件：`ReportArchiveView.vue`
- 预览：[打开](preview.html?screen=36)；截图：[PNG](renders/36-report-archives.png)
- 视觉基准：ImageGen统一视觉系统；逐页预览为完整内容与桌面构图依据。

Heading 历史报告归档. Dense archival list table 归档时间、采集任务、采用运行批次、范围、生成状态、操作. Side selected archive preview summary with immutable capture time, rules/standard revision and sources, clear 历史快照 label. Actions 预览 / 导出HTML / 导出JSON. Failed generation has retry explanation if existing API allows, otherwise 查看失败原因; do not invent a re-run API. Retention scope shown as bounded limit, current source status does not rewrite archive.

## 37 合规报告打印版

- 模块：报告审计
- 入口：`reports:print`
- 现有组件：`ComplianceReportView.vue`
- 预览：[打开](preview.html?screen=37)；截图：[PNG](renders/37-print-report.png)
- 视觉基准：ImageGen统一视觉系统；逐页预览为完整内容与桌面构图依据。

Create a single clean A4 portrait PRINT REPORT page, NO application sidebar, NO top application toolbar. White page with deep indigo body type and a small coral feather wordmark 赤翎 / SCARLET PLUME at top. Title 数据安全合规报告, scope/date/report ID with 示例报告 label. Structured sections 采用范围、来源与识别依据、检查与整改复测、独立复核、证据索引. A narrow teal/blue feather-spine down left ties numbered sections. Legible tables, clear gaps/unknown/stale labels, page number and frozen capture time in footer. No background ink-heavy graph, no invented legal seal, no claim of overall certification. This is the print variant of the same product.

## 38 审计中心与操作详情

- 模块：报告审计
- 入口：`audit/actions`
- 现有组件：`AuditView.vue`
- 预览：[打开](preview.html?screen=38)；截图：[PNG](renders/38-audit.png)
- 视觉基准：ImageGen统一视觉系统；逐页预览为完整内容与桌面构图依据。

Heading 审计中心. Tabs 平台操作 / 请求审计. Filter actor/action/object/date; table 时间、操作者、操作、对象、来源、结果. Selected row details rail shows before/after version, adopted scope, correlation reference, immutable timestamps. Sensitive settings show 已隐藏 rather than plaintext. Distinguish successful action from successful risk remediation. Export interface if available and paginated history; no raw credentials, SQL or request body.

## 39 多源自动发现

- 模块：配置接入
- 入口：`discovery`
- 现有组件：`DiscoveryView.vue`
- 预览：[打开](preview.html?screen=39)；截图：[PNG](renders/39-discovery.png)
- 视觉基准：ImageGen统一视觉系统；逐页预览为完整内容与桌面构图依据。

Heading 多源自动发现. Source tiles as compact horizontal rows PostgreSQL / MySQL / MariaDB / MongoDB / OpenMetadata with configured status, interval, latest actual run, coverage and queue state. Actions 新增来源 / 测试连接 / 立即采集. Main source list and lower versioned recognition rule table. Right selected source run proof shows metadata/content/permissions/lineage coverage separately, unknown permission remains unknown, no inference from OpenMetadata ownership tags. Small source-to-catalog feather path connects intake→field→rule→G02 evidence.

## 40 发现任务配置与频率

- 模块：配置接入
- 入口：`discovery:source-edit`
- 现有组件：`DiscoveryView.vue`
- 预览：[打开](preview.html?screen=40)；截图：[PNG](renders/40-discovery-form.png)
- 视觉基准：ImageGen统一视觉系统；逐页预览为完整内容与桌面构图依据。

Heading 配置自动发现来源. Form source alias, provider, database type, database name, schema/collection scope, OpenMetadata service alias when appropriate, interval, enabled flag and current revision. Credentials remain customer-side: show environment-reference names, never token values or passwords. Right live connection diagnostics and scope summary distinguishes 连接诊断 from actual 采集成功. Sticky 保存配置 / 测试连接 / 立即采集; queued/inflight and revision-conflict draft-preserved states as small examples.

## 41 自定义识别规则

- 模块：配置接入
- 入口：`discovery:rules`
- 现有组件：`DiscoveryView.vue`
- 预览：[打开](preview.html?screen=41)；截图：[PNG](renders/41-discovery-rules.png)
- 视觉基准：ImageGen统一视觉系统；逐页预览为完整内容与桌面构图依据。

Heading 自定义识别规则. Left rules list built-in and customer versions, 草稿/发布/退役 state. Center new-version editor rule key/name, type 关键词 / RE2正则 / 精确词典, target 元数据/内容, candidate category, expression/dictionary values using harmless department example. Right example-match preview is labeled 示意, not claimed new implementation feature; instead keep a compact explanation and adopted-batch link if no preview API. Footer 保存草稿 / 发布版本. Lower rule-hit evidence table version/count/denominator/source, raw content never displayed.

## 42 自动采集任务台账

- 模块：配置接入
- 入口：`automation`
- 现有组件：`AutomationView.vue`
- 预览：[打开](preview.html?screen=42)；截图：[PNG](renders/42-automation.png)
- 视觉基准：ImageGen统一视觉系统；逐页预览为完整内容与桌面构图依据。

Heading 自动采集. Task list source/provider/authorized scope/frequency/next run/latest status, separate manual queue state and enabled/paused. Primary 新建任务. Selected task opens schedule/coverage/run history rail, actions 立即采集 / 编辑频率 / 查看报告归档. Scope shows only allowed source and tenant. Read-only viewer no write controls. Token state has only masked placeholder, one-time-copy flow separate.

## 43 PostgreSQL连接配置与测试

- 模块：配置接入
- 入口：`automation:managed-pg`
- 现有组件：`ManagedPostgresForm.vue`
- 预览：[打开](preview.html?screen=43)；截图：[PNG](renders/43-postgres-connection.png)
- 视觉基准：ImageGen统一视觉系统；逐页预览为完整内容与桌面构图依据。

Heading PostgreSQL 自动采集配置. Labeled fields 已允许主机、端口、数据库、只读账号、密码、schema范围、采集频率. Production secrets masked and optional saved-secret reference, no sample real password. Three-stage vertical diagnostics DNS/TLS与连接 → 元数据权限 → 可采集范围. Successful connection says 连接测试成功 not already采集通过. Right minimal least-privilege scope summary for fields/content/visible ACL; test result unknown/denied is distinct. Footer 测试连接 / 保存任务.

## 44 采集运行记录与归档

- 模块：配置接入
- 入口：`automation:history`
- 现有组件：`AutomationView.vue`
- 预览：[打开](preview.html?screen=44)；截图：[PNG](renders/44-automation-runs.png)
- 视觉基准：ImageGen统一视觉系统；逐页预览为完整内容与桌面构图依据。

Heading 采集运行记录. Selected task context and immutable scope/revision header. List recent runs with 运行批次、触发方式、开始/结束、采集结果、资产变更、检查证据、报告归档. Right selected successful/failed run detail with known result code and coverage gaps, linked G02/G03 evidence and report archive. Show source observedAt distinct from createdAt, source retries idempotent, no raw DSN or private exception text.

## 45 检查模板目录与版本详情

- 模块：配置接入
- 入口：`templates`
- 现有组件：`TemplatesView.vue TemplateCriteria.vue`
- 预览：[打开](preview.html?screen=45)；截图：[PNG](renders/45-templates.png)
- 视觉基准：ImageGen统一视觉系统；逐页预览为完整内容与桌面构图依据。

Heading 检查模板. Left template catalog scenario filters sharing/cross-border/AI/general. Center selected published template detail title, version, adopted requirement links. Each check item displays distinct sections 适用条件 / 取证要求 / 验收标准 and associated requirement. Right publication history, immutable adopted-version summary and actions 基于此版修订 / 新建模板. No silent redefinition of published checks.

## 46 检查模板新建与修订

- 模块：配置接入
- 入口：`templates/new + templates/revise/:id`
- 现有组件：`TemplateForm.vue`
- 预览：[打开](preview.html?screen=46)；截图：[PNG](renders/46-template-form.png)
- 视觉基准：ImageGen统一视觉系统；逐页预览为完整内容与桌面构图依据。

Heading 修订检查模板. Current published version v2 and new draft v3 shown separately. Left item outline, central fields 模板名称、适用场景、检查项标题、监管条款、适用条件、取证要求、验收标准. Right live-looking static preview of chosen item and version note; preserve actual form capabilities. Footer 保存并发布新版本 / 取消, explicit old adopted checks retain old revision. Use readable large textareas and subtle feather section spine.

## 47 集成目录与资产导入

- 模块：配置接入
- 入口：`integrations`
- 现有组件：`WorkspaceView.vue ImportPanel.vue`
- 预览：[打开](preview.html?screen=47)；截图：[PNG](renders/47-integrations-import.png)
- 视觉基准：ImageGen统一视觉系统；逐页预览为完整内容与桌面构图依据。

Heading 集成与导入. Main integration directory with provider, supported scope, status and documentation link, not all connectors marked verified. Right import panel file/JSON schema information, source aliases, validation summary. Lower import result table 新增/更新/跳过/错误 and row-specific errors; no private data samples. Preserve actual provider/JSON acceptance contract; do not invent CSV or unimplemented OAuth setup. Actions 验证导入内容 / 执行导入 only if mapped to existing capabilities.

## 48 登录赤翎

- 模块：入口系统
- 入口：`login`
- 现有组件：`App.vue`
- 预览：[打开](preview.html?screen=48)；截图：[PNG](renders/48-login.png)
- 视觉基准：ImageGen统一视觉系统；逐页预览为完整内容与桌面构图依据。

Create the LOGIN screen of the same brand. No authenticated sidebar. Split page: midnight-indigo left 55% with an elegant precise feather of data relationship paths and coral feather logo 赤翎 / SCARLET PLUME; pale white right45% with a compact login form. Heading 登录赤翎, text 使用管理员分配的工作区账号, fields 账号 / 密码, primary 登录 and no invented social login, no public registration. Tiny source/evidence nodes in the feather express traceability, not fake live counts. Include discreet inline invalid-login feedback state below password and busy button state shown as a small separate inset only if space. Strong contrast and generous space.

## 49 工作区选择与访问状态

- 模块：入口系统
- 入口：`workspace-selector`
- 现有组件：`App.vue`
- 预览：[打开](preview.html?screen=49)；截图：[PNG](renders/49-workspaces.png)
- 视觉基准：ImageGen统一视觉系统；逐页预览为完整内容与桌面构图依据。

Create the WORKSPACE SELECTION screen. Same minimal public top bar coral feather mark 赤翎 / SCARLET PLUME and user/logout control, no authenticated sidebar. White/pale-blue working area. Heading 选择工作区 with role-aware rows 企业数据治理工作区 / 研发测试工作区, slug, role pill 负责人/只读 and clear enter arrow. Side quiet abstract feather branches each representing an authorized workspace. Small alternate state examples 暂无可访问的工作区 / 此链接中的工作区不可访问 with admin-contact text, not a fake create-workspace self-service button.

## 50 导航与基础组件规范

- 模块：组件状态
- 入口：`system:components`
- 现有组件：`App.vue Brand.vue Icon.vue`
- 预览：[打开](preview.html?screen=50)；截图：[PNG](renders/50-shell-components.png)
- 视觉基准：ImageGen统一视觉系统；逐页预览为完整内容与桌面构图依据。

Create a professional DESIGN SYSTEM SPECIMEN BOARD for the SAME brand, not a live data page. Pale background, title 赤翎 · 基础组件. Left shows FULL compact sidebar navigation groups and ALL19 entries: 概览; 数据资产、业务资产目录、分类分级、数据流转、处理活动; 运营与处置、API安全、API主动验证、接口生命周期、运行时检测策略、监管检查、合规评估、合规报告、审计中心; 多源自动发现、自动采集、检查模板、集成与导入. It is a scrollable sidebar with clear active and focus states. Right organized specimens: blue primary/secondary/disabled/loading buttons, focus rings, tabs, table selected/hover rows, severity/status chips with TEXT, breadcrumbs, pagination, source icons and subtle feather-spine divider. Exact palette indigo/blue/teal/violet/coral and Source Han Sans SC. No fictional interaction features.

## 51 表单抽屉与确认状态规范

- 模块：组件状态
- 入口：`system:forms-dialogs`
- 现有组件：`shared forms`
- 预览：[打开](preview.html?screen=51)；截图：[PNG](renders/51-forms-overlays.png)
- 视觉基准：ImageGen统一视觉系统；逐页预览为完整内容与桌面构图依据。

Create a cohesive SPECIMEN BOARD with four large readable panels, all same brand: 1 labeled form with required indicator, helper and inline error; 2 right-side detail drawer with heading, close control, scroll area and sticky footer; 3 independent approval confirmation dialog with read-only target/scope/revision and separate reviewer reason; 4 one-time token success dialog with masked placeholder dots, 复制令牌 / 我已保存 controls and no real secrets. Title 赤翎 · 表单与操作状态. Include version conflict message 草稿已保留，请重新核对当前版本 and an explicit read-only role hint. Correct accessible contrast, 44px touch affordances, consistent border radius and no giant decorative feather.

## 52 加载空白失败与权限状态

- 模块：组件状态
- 入口：`system:states`
- 现有组件：`shared views`
- 预览：[打开](preview.html?screen=52)；截图：[PNG](renders/52-states.png)
- 视觉基准：ImageGen统一视觉系统；逐页预览为完整内容与桌面构图依据。

Create a DESIGN STATE BOARD with six clear panels in a 3x2 grid under 赤翎 · 系统状态. Each panel is a mini but legible app content area: 加载中 skeleton with subdued feather shaft; 尚未接入来源 with one primary 接入数据源; 筛选无结果 with 清除筛选; 读取失败 with 重试 and retained last-known timestamp; 权限不足 with role explanation and 返回; 来源过期 / 部分覆盖 / 未知条件 with specific badges and scope notes. Unknown is neutral, failure coral, confirmed-safe teal, never color-only. No fake 404 buttons for APIs, no automatic success states. Add small session expired/login prompt variant in footer.

## 57 业务属性确认与CMDB冲突

- 模块：数据治理
- 入口：`asset-catalog:metadata/cmdb`
- 现有组件：`AssetCatalogView.vue`
- 预览：[打开](preview.html?screen=57)；截图：[PNG](renders/57-catalog-conflict.png)
- 视觉基准：ImageGen统一视觉系统；逐页预览为完整内容与桌面构图依据。

Heading 业务属性确认与来源冲突. Split master/detail white surfaces. Main chosen catalog item 客户服务 / inventory.classification_contacts, tabs 自动观察 / 人工确认 / 变更历史. An explicit before/observed/confirmed comparison table for 责任团队、所属业务、地域、共享对象、保留期限, with conflict on 保留期限 and current manual value preserved. Right metadata confirmation form and note; small lower CMDB JSON import panel showing only synthetic reference keys sourceRef/assetId and error row for cross-workspace object. No personal values or arbitrary SQL. Actions 确认属性 / 查看历史 / 导入CMDB, revision conflict retains draft.

## 必须遵守的业务契约

- 数据属性仅采用既有业务数据、个人信息、敏感个人信息；业务保护级别仅采用现有内部、机密；法定类别沿既有待认定/一般/重要/核心，不能把个人信息当成法定级别。
- 来源元数据、内容统计、可见ACL和血缘覆盖分别展示。OpenMetadata标签、所有者不等于源数据库权限证据。
- 模式信号、连接诊断、来源复测和独立通过分开；不凭HTTP200、没有新报警或执行回执自动通过。
- 输入、权限、版本冲突和后端门槛保持原业务约束；页面不得新增图稿中虚构的API、导航模块、预览引擎、评分或认证。
- 原始观察、整改回执与新复测分别显示当时内容，不把新结果放回旧快照。
- 所有按钮与表单在本设计包里只做视觉预览，不发送API；开发时逐项绑定既有接口。
