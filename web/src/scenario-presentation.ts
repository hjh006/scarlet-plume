import type { ScenarioCheckRun } from './scenario-checks'

export const scenarioPresentations: Record<string, { title: string; focus: string; layout: string; keys: string[]; next: string; action: string }> = {
  'personal/inventory': { title: '个人信息在哪里使用？', focus: '核对数据、用途、接收方和保存安排', layout: 'personal-chain', keys: ['source_observed_types', 'activity_purpose', 'activity_recipient', 'activity_retention'], next: 'activities', action: '核对处理活动' },
  'personal/pia': { title: '处理影响有哪些依据，保护还缺什么？', focus: '必要性、权益影响与技术保护分开核实', layout: 'impact', keys: ['lawful_basis', 'activity_purpose', 'automated_decision', 'activity_safeguards'], next: 'assessments', action: '进入影响评估' },
  'personal/audit': { title: '审计底稿能追溯到哪些技术证据？', focus: '逐项核对依据、来源、缺项和独立复核', layout: 'audit', keys: ['scenario_assessment', 'scenario_check_records', 'lawful_basis'], next: 'checks', action: '查看审计检查记录' },
  'personal/rights-retention': { title: '保存安排与实际执行，还差哪些证据？', focus: '告知登记、保留安排和删除执行各自取证', layout: 'retention', keys: ['notice_consent', 'activity_retention', 'application_retention'], next: 'activities', action: '核对保存与权利安排' },
  'domestic/applicability': { title: '本次应核对哪些要求？', focus: '先确认组织、数据身份及采用要求', layout: 'applicability', keys: ['cii_status', 'asset_statutory_category', 'classification_standard', 'lawful_basis'], next: 'templates', action: '核对采用要求' },
  'domestic/self-check': { title: '本次检查，还缺哪些证据？', focus: '技术要求、实际观察与受控整改一一对应', layout: 'controls', keys: ['source_permissions', 'activity_safeguards'], next: 'checks', action: '打开检查工作台' },
  'domestic/important-data': { title: '重要数据的身份与保护依据明确吗？', focus: '法定认定、企业级别与保护证据分别核对', layout: 'classification', keys: ['asset_statutory_category', 'classification_standard', 'asset_business_level', 'activity_safeguards'], next: 'classification', action: '核对分类认定依据' },
  'domestic/personal-audit': { title: '个人信息审计，有哪些可采用底稿？', focus: '与个人信息合规审计共用同一检查任务', layout: 'audit', keys: ['scenario_assessment', 'scenario_check_records', 'lawful_basis'], next: 'scenarios/personal/audit', action: '打开个人信息审计入口' },
  'domestic/inspection': { title: '迎检材料与技术发现能够对上吗？', focus: '材料形成、证据引用和外部程序分别列示', layout: 'packages', keys: ['classification_publication', 'scenario_check_records', 'scenario_assessment'], next: 'reports', action: '核对报告实际采用范围' },
  'crossborder/scope': { title: '数据去向，哪些有登记，哪些有观察？', focus: '登记路径与来源关系分层核对', layout: 'transfer', keys: ['activity_transfer', 'activity_recipient', 'activity_region', 'deduplicated_subject_count'], next: 'data-flows', action: '调查来源关系与断点' },
  'crossborder/assessment': { title: '出境范围与保护措施，有哪些缺口？', focus: '规模事实、批准范围与技术观察分别比较', layout: 'impact', keys: ['activity_recipient', 'deduplicated_subject_count', 'activity_safeguards', 'scenario_assessment'], next: 'assessments', action: '进入数据出境评估' },
  'crossborder/procedures': { title: '拟采用的出境路径，还缺哪些事实？', focus: '按真实身份和条件核对程序材料，不自动认定路径', layout: 'procedures', keys: ['cii_status', 'cross_border_exception', 'deduplicated_subject_count', 'asset_statutory_category'], next: 'reports', action: '查看路径材料与缺项' },
  'crossborder/review': { title: '业务修订之后，哪些证据需要复查？', focus: '活动修订、原运行与新来源分别保留', layout: 'revision', keys: ['activity_recipient', 'activity_transfer', 'scenario_assessment'], next: 'activities', action: '核对活动修订历史' },
  'governance/inventory': { title: '哪些数据还没有可靠的分类？', focus: '先看已授权来源和实际覆盖', layout: 'coverage', keys: ['source_metadata', 'source_observed_types', 'source_permissions', 'source_coverage'], next: 'discovery', action: '核对发现来源与覆盖' },
  'governance/classification': { title: '新观察与已采用分类一致吗？', focus: '候选、人工确认、独立发布和接收回执分别核对', layout: 'classification', keys: ['classification_candidates', 'classification_context', 'classification_publication', 'classification_standard'], next: 'classification', action: '进入分类台账核对' },
  'governance/access': { title: '实际授权符合批准的访问范围吗？', focus: '只在来源覆盖的准确对象上比较权限', layout: 'permissions', keys: ['source_permissions', 'activity_safeguards'], next: 'assets', action: '查看资产与权限依据' },
  'governance/lifecycle': { title: '数据保存与退出，有没有执行依据？', focus: '登记安排、源端执行和备份处理各自取证', layout: 'retention', keys: ['activity_retention', 'application_retention', 'activity_safeguards'], next: 'activities', action: '核对生命周期安排' },
  'ai/assessment': { title: '本次 AI 使用，应核对哪些条件？', focus: '业务用途、服务对象和采用数据决定核查范围', layout: 'applicability', keys: ['application_service_audience', 'public_ai_service', 'activity_purpose', 'application_data_categories'], next: 'assessments', action: '核对 AI 使用评估' },
  'ai/data': { title: '哪些数据正在跨过 AI 使用边界？', focus: '输入、知识与输出分别对照配置和观察', layout: 'ai-lanes', keys: ['application_data_categories', 'application_knowledge_bases', 'application_training_use', 'application_retention'], next: 'runtime-policies', action: '核对企业出口策略' },
  'ai/suppliers-tools': { title: '供应商与工具，分别接收哪些数据？', focus: '模型服务、外部工具和知识服务分别核对', layout: 'suppliers', keys: ['application_vendor', 'application_model', 'application_region', 'application_tools', 'application_training_use'], next: 'activities', action: '核对供应商与处理登记' },
  'ai/public-service': { title: '公众服务的实际范围和程序明确吗？', focus: '公开入口、内容处理和正式凭证分别取证', layout: 'public-service', keys: ['public_ai_service', 'application_service_audience', 'notice_consent', 'scenario_assessment'], next: 'assessments', action: '核对公众服务条件' },
  'sharing/providing': { title: '接收方的实际访问，符合批准范围吗？', focus: '接收方、用途、数据与观察一一对照', layout: 'receivers', keys: ['activity_recipient', 'activity_purpose', 'activity_transfer', 'source_permissions'], next: 'activities', action: '核对接收关系' },
  'sharing/outsourcing': { title: '委托指令与实际处理，有哪些差异？', focus: '委托目的、受托权限、再委托与退出责任分开', layout: 'outsourcing', keys: ['activity_purpose', 'activity_recipient', 'activity_safeguards', 'activity_retention'], next: 'activities', action: '核对委托指令与责任' },
  'sharing/permissions': { title: '共享权限差异，能定位到准确目标吗？', focus: '主体、接口与数据类别不得扩大为更大授权', layout: 'permissions', keys: ['activity_recipient', 'source_permissions', 'activity_safeguards'], next: 'operations', action: '核对受控处置目标' },
  'sharing/exit': { title: '关系终止后，权限与数据是否已退出？', focus: '终止、撤权、新观察和返还删除各自取证', layout: 'exit', keys: ['activity_recipient', 'activity_retention', 'source_permissions'], next: 'activities', action: '核对退出事实与执行依据' },
  'incident/investigation': { title: '发生了什么，下一步该核实哪里？', focus: '来源、明确对象与观察窗口决定调查边界', layout: 'timeline', keys: ['source_coverage', 'scenario_check_records'], next: 'audit', action: '调查原始来源记录' },
  'incident/response': { title: '处置到哪一步，还在等待什么？', focus: '申请、批准、来源执行、回滚与冲突分别查看', layout: 'actions', keys: ['source_permissions', 'activity_safeguards'], next: 'operations', action: '进入受控处置工作区' },
  'incident/reporting': { title: '事件材料中，哪些已知、哪些仍未知？', focus: '技术底稿和正式提交凭证分别保留', layout: 'packages', keys: ['scenario_check_records', 'scenario_assessment', 'source_coverage'], next: 'capabilities?tab=reports', action: '生成指定范围的技术报告' },
  'incident/retest': { title: '措施有效吗，有没有同范围新观察？', focus: '旧事实重查、新来源复测与独立核验分开', layout: 'retest', keys: ['scenario_check_records', 'source_coverage'], next: 'checks', action: '核对关联检查与独审' },
}

export const defaultScenarioTasks: Record<string, string> = { personal: 'inventory', domestic: 'self-check', crossborder: 'scope', governance: 'inventory', ai: 'data', sharing: 'providing', incident: 'investigation' }

export function scenarioStages(run?: ScenarioCheckRun) {
  const actions = run?.actions ?? []
  return [
    { label: '采用来源', state: run?.evidence?.length ? '已保存引用' : '尚未采用', reached: !!run?.evidence?.length },
    { label: '技术检查', state: run ? '已保存运行 #' + run.id : '尚未运行', reached: !!run },
    { label: '受控整改', state: actions.length ? actions.length + ' 项申请' : '尚无申请', reached: !!actions.length },
    { label: '来源执行', state: actions.some(a => a.status === 'applied') ? '已收到执行回执' : actions.some(a => ['failed', 'conflict'].includes(a.status)) ? '失败或冲突，需核对' : '尚无已执行回执', reached: actions.some(a => a.status === 'applied') },
    { label: '新来源复测', state: run?.retestState === 'new_source_observation' ? '已采用新来源' : run?.retestState === 'same_source_observation' ? '旧事实重查' : '尚无新来源复测', reached: run?.retestState === 'new_source_observation' },
    { label: '独立复核', state: '在关联检查中核对', reached: false },
  ]
}
