# 桌面实施验收（2026-11-12）

依据已批准的53项设计，完成19个现有一级模块及其详情、表单、历史、审批、打印与公共状态。50–52以真实共享组件状态覆盖；没有新增同名生产导航。手机端延期，浏览器打标排除。

## 范围与证据口径

- 机器可读清单见[implementation.json](implementation.json)。以下勾选表示实施完成并有对应截图和限定功能证据，不表示53项均由独立评审逐张人工检查，也不表示所有对接厂商能力已经完整实现。
- 本地验收材料根目录：`/Users/simon/.codex/visualizations/2026/11/12/scarlet-desktop-implementation`。截图、PDF和含合成业务的导出保留在该目录，不批量提交到开源仓库。JSON内路径相对此目录；完整文档截图与固定首屏截图分别记录。
- 基准视口1600×1000、1366×900、1280×900。数据组早期1040px高截图已补固定视口；安全组原长图的viewport符合基准，另补08固定首屏。具体参数见各组QA。
- 实际API读取和业务写入不混称：local工作区在UI验收中只读；真实写操作仅发生在隔离UI工作区。加载/503与部分409采用浏览器模拟的项目在清单中明确标记，未当作真实业务成功。
- 独立评审对照批准设计抽查核心页面及50–52状态，确认图谱可读性/碰撞、资产列宽、筛选控件与登录对齐的修复，打印页级追溯的开发环境视觉也成立。其指出的生产打印待验条件，由发布负责人完成下述真实CSP验证后关闭。其余项由实施方逐项截图、布局及功能检查；没有声称全53项独立人工视觉审阅。

## 实际操作确认

隔离工作区完成G01取证、退回、整改、晚于整改的新复测、再次提交及独立通过。真实PIA保留14个未知事实与5个缺口，由独立复核人退回，没有伪造通过。CMDB并发409保持编辑草稿；策略修订、只读角色、令牌掩码及关闭清除均经过实际会话。处置申请人自批被403拒绝，独立审批后停留“等待来源执行”，没有配置执行Agent或对应用实施阻断。验收结束已删除本轮隔离工作区和3个专用账号，截图与QA材料保留；清理仅影响隔离夹具，未删除或手动更改local记录，后台采集继续运行。

本地Docker发布后再次检查19个导航及关键深链接、真实OpenMetadata来源、Wazuh已复核检查37和合规导出。OpenMetadata仍仅证明元数据采集，内容为not_collected、源ACL未知；Wazuh样例保持403。原归档382在此前联调中曾通过不可变性核对；本轮它已超出既有每任务30份的自动保留窗口，读取404。改用当前仍保留归档做两次读取的摘要一致性核对；不声称已删除归档在发布后仍存在，也不把PostgreSQL任务归档冒充Wazuh归档。发布记录见`deployed/report.json`，真实厂商闭环见[此前验收](../../acceptance-live-integrations-2026-11-11.md)。

## 验证与限制

Vue类型检查和生产构建通过。机械设计检测仅执行一次，47个变更前端文件有7项样式提示；逐项复看后保留其实际选中状态、缺证据警示、一次性凭据和采用版本语义，并记录理由，没有把检测提示当作业务缺陷或声称零提示。独立复核记录随本地材料保留。构建保留既有主包体积提示，不影响构建结果。GitHub当前提交的Go/Django/PostgreSQL及原生采集CI须全部完成后交付。

生产CSP禁止内联style，开发预览生成PDF最初未暴露此限制；发布烟测实际发现后，改用组件管理的原生构造样式表（CSSStyleSheet/adoptedStyleSheets），保留后端CSP。再次从8890生成当前12页、历史6页PDF，逐页验证范围、真实时点和页码；CSP违规0、组件卸载后样式表清理通过，见`deployed/print-report.json`。打印使用当前Chrome支持的CSS分页margin boxes。其他浏览器的打印引擎未在本轮逐一验收。当前预览与历史采用快照区分，未构造冻结状态或报告ID。

## 53项清单

| 完成 | ID | 页面或状态 | 实现组件 | 功能验收范围 |
| --- | --- | --- | --- | --- |
| [x] | 1 | 数据安全全景 | WorkspaceOverview.vue | 实际图谱读取、节点选择与依据 |
| [x] | 2 | 数据资产台账 | WorkspaceView.vue | 实际资产检索、无结果、登记表聚焦；仅本地UI状态变化 |
| [x] | 3 | 检查与独立复核 | CheckDetail.vue | 隔离工作区真实G01：取证→退回→整改→新复测→再次提交→独立通过 |
| [x] | 4 | 业务与数据资产目录 | AssetCatalogView.vue | 实际接口读取、页面操作与权限展示；未修改local业务记录 |
| [x] | 5 | 分类分级台账 | ClassificationView.vue | 实际接口读取、页面操作与权限展示；未修改local业务记录 |
| [x] | 6 | 分类分级标准库 | ClassificationStandardsView.vue | 实际接口读取、页面操作与权限展示；未修改local业务记录 |
| [x] | 7 | 分类确认与自动判定依据 | ClassificationView.vue ClassificationEvaluation.vue | 实际规则、采集事实、证据批次、缺失条件页签；未写正式分类 |
| [x] | 8 | 数据流转图谱 | DataFlowsView.vue | 实际节点与边、键盘、邻接、缩放、重置及标签避让 |
| [x] | 9 | 图谱历史与版本差异 | DataFlowsView.vue | 实际历史快照读取与差异对比 |
| [x] | 10 | 请求与数据关系调查 | DataFlowsView.vue | 同Trace请求调查及来源元数据对齐 |
| [x] | 11 | 处理活动台账 | ActivitiesView.vue | 实际接口读取、页面操作与权限展示；未修改local业务记录 |
| [x] | 12 | 新建与编辑处理活动 | ActivityCreate.vue ActivityDetail.vue | AI/出境条件表单、编辑及取消；未写local活动 |
| [x] | 13 | 处理活动详情与变更历史 | ActivityDetail.vue | 实际接口读取、页面操作与权限展示；未修改local业务记录 |
| [x] | 14 | 安全运营与处置概览 | SecurityOperationsView.vue | 实际接口读取、页面操作与权限展示；未修改local业务记录 |
| [x] | 15 | 安全事件台账 | SecurityOperationsView.vue | 实际接口读取、页面操作与权限展示；未修改local业务记录 |
| [x] | 16 | 处置动作与执行回执 | SecurityOperationsView.vue | 实际既有处置及回执读取；本轮未重新执行物理处置 |
| [x] | 17 | 处置申请与独立审批 | SecurityOperationsView.vue | 隔离合成范围真实申请、申请人自批403、独立审批；停留approved无执行Agent |
| [x] | 18 | 事件与执行来源配置 | SecurityOperationsView.vue | 隔离真实一次性令牌：掩码、复制、关闭清除、撤销及viewer禁写 |
| [x] | 19 | API契约安全与扫描 | ApiSecurityView.vue | 实际接口读取、页面操作与权限展示；未修改local业务记录 |
| [x] | 20 | API契约扫描详情 | ApiSecurityView.vue | 实际接口读取、页面操作与权限展示；未修改local业务记录 |
| [x] | 21 | 运行时观测与来源 | ApiRuntimeView.vue | 实际接口读取、页面操作与权限展示；未修改local业务记录 |
| [x] | 22 | API流量监测与请求审计 | RuntimeMonitor.vue AuditView.vue | 实际来源筛选与请求记录依据对齐，审计详情读取 |
| [x] | 23 | 运行时风险研判与分派 | RuntimeFindingsPanel.vue RuntimeRiskEditor.vue RuntimeDetectionFacts.vue | 实际接口读取、页面操作与权限展示；未修改local业务记录 |
| [x] | 24 | API主动验证任务 | ApiValidationView.vue | 实际任务台账、详情与返回台账 |
| [x] | 25 | 主动验证范围配置与审批 | ApiValidationView.vue | 配置与旧结果分离、viewer禁建；本轮不再次发起主动探测 |
| [x] | 26 | 主动验证结果与判定依据 | ApiValidationView.vue | 实际接口读取、页面操作与权限展示；未修改local业务记录 |
| [x] | 27 | API生命周期与版本变更 | ApiLifecycleView.vue | 实际服务范围、生命周期导出与无结果状态 |
| [x] | 28 | 运行时检测策略与版本 | RuntimePoliciesView.vue | 隔离策略真实修订、版本历史、409保留草稿及viewer只读 |
| [x] | 29 | 监管检查台账 | ChecksView.vue | 实际接口读取、页面操作与权限展示；未修改local业务记录 |
| [x] | 30 | 新建监管检查 | CheckCreate.vue TemplateCriteria.vue | 实际接口读取、页面操作与权限展示；未修改local业务记录 |
| [x] | 31 | 证据登记与来源取证 | CheckEvidenceForm.vue CheckDetail.vue | 隔离真实观察/整改/复测证据；另以模拟409验证草稿保留 |
| [x] | 32 | 专项合规评估台账 | AssessmentsView.vue | 实际接口读取、页面操作与权限展示；未修改local业务记录 |
| [x] | 33 | 专项评估答复与独立复核 | AssessmentsView.vue | 隔离真实PIA保存、下载、提交、独立退回；14个unknown与5个缺口阻止通过 |
| [x] | 34 | 专项评估模板编辑 | AssessmentsView.vue | 实际接口读取、页面操作与权限展示；未修改local业务记录 |
| [x] | 35 | 合规报告预览与导出 | ComplianceReportView.vue | 实际预览与HTML/JSON导出；范围、来源和未知项保持 |
| [x] | 36 | 历史报告归档 | ReportArchiveView.vue | 实际历史归档读取与导出摘要一致；旧382已超既有30份保留窗口 |
| [x] | 37 | 合规报告打印版 | ComplianceReportView.vue | 实际多页A4 PDF，页码、范围及真实生成时点逐页验证 |
| [x] | 38 | 审计中心与操作详情 | AuditView.vue | 实际接口读取、页面操作与权限展示；未修改local业务记录 |
| [x] | 39 | 多源自动发现 | DiscoveryView.vue | 真实来源和运行记录读取；本轮不修改local采集任务 |
| [x] | 40 | 发现任务配置与频率 | DiscoveryView.vue | 真实表单读取及模拟PUT409保留草稿；未修改local频率 |
| [x] | 41 | 自定义识别规则 | DiscoveryView.vue | 实际接口读取、页面操作与权限展示；未修改local业务记录 |
| [x] | 42 | 自动采集任务台账 | AutomationView.vue | 实际接口读取、页面操作与权限展示；未修改local业务记录 |
| [x] | 43 | PostgreSQL连接配置与测试 | ManagedPostgresForm.vue | 实际接口读取、页面操作与权限展示；未修改local业务记录 |
| [x] | 44 | 采集运行记录与归档 | AutomationView.vue | 实际接口读取、页面操作与权限展示；未修改local业务记录 |
| [x] | 45 | 检查模板目录与版本详情 | TemplatesView.vue TemplateCriteria.vue | 实际接口读取、页面操作与权限展示；未修改local业务记录 |
| [x] | 46 | 检查模板新建与修订 | TemplateForm.vue | 实际接口读取、页面操作与权限展示；未修改local业务记录 |
| [x] | 47 | 集成目录与资产导入 | WorkspaceView.vue ImportPanel.vue | 真实集成状态；清单本地校验、确认入口，不执行导入 |
| [x] | 48 | 登录赤翎 | App.vue | 真实有效/无效登录、密码清除、外域Origin403 |
| [x] | 49 | 工作区选择与访问状态 | App.vue | 真实授权工作区选择与越权链接状态 |
| [x] | 50 | 导航与基础组件规范 | App.vue Brand.vue Icon.vue | 19个实际导航、角色和焦点；组件规格不新增导航 |
| [x] | 51 | 表单抽屉与确认状态规范 | shared forms | 原生快捷入口的键盘/焦点/Escape，真实掩码令牌及确认状态 |
| [x] | 52 | 加载空白失败与权限状态 | shared views | 实际空结果/只读/越权/版本冲突；仅加载和503会话读取状态使用明确标注的浏览器模拟 |
| [x] | 57 | 业务属性确认与CMDB冲突 | AssetCatalogView.vue | 实际CMDB来源与确认值冲突；并发409保留草稿，显式重载替换 |
