export const toolGroups = [
  { label: '工作区概况', items: [
    { id: 'overview', label: '数据安全全景', icon: 'overview', description: '查看当前工作区的资产、来源、发现和检查概况。' },
  ] },
  { label: '数据治理', items: [
    { id: 'assets', label: '数据资产', icon: 'assets', description: '查看资产台账、来源引用和数据库字段与权限。' },
    { id: 'asset-catalog', label: '业务资产目录', icon: 'assets', description: '核对业务目录、责任信息与来源确认记录。' },
    { id: 'classification', label: '分类分级', icon: 'classification', description: '查看分类结果、采用标准、认定依据和历史。' },
    { id: 'data-flows', label: '数据流转', icon: 'data-flows', description: '调查有来源依据的节点、关系和历史快照。' },
    { id: 'activities', label: '处理活动', icon: 'activities', description: '登记业务用途、处理范围及相关变更。' },
  ] },
  { label: '安全运营与核验', items: [
    { id: 'operations', label: '运营与处置', icon: 'shield', description: '核实事件、申请处置并检查执行与复测回执。' },
    { id: 'api-security', label: 'API 安全', icon: 'api-security', description: '核对接口契约、运行时观测与检测发现。' },
    { id: 'validation', label: 'API 主动验证', icon: 'shield', description: '查看已授权范围的验证计划、运行和结果依据。' },
    { id: 'api-lifecycle', label: '接口生命周期', icon: 'api-lifecycle', description: '对照接口声明、实际观测及变更情况。' },
    { id: 'runtime-policies', label: '运行时检测策略', icon: 'templates', description: '维护检测策略参数、修订与采用版本。' },
  ] },
  { label: '检查、评估与交付', items: [
    { id: 'checks', label: '监管检查', icon: 'checks', description: '开展取证、整改、新复测和独立复核。' },
    { id: 'assessments', label: '合规评估', icon: 'checks', description: '查看个人信息、出境、AI及业务变更评估。' },
    { id: 'reports', label: '合规报告', icon: 'reports', description: '查看当前范围报告、历史归档与导出材料。' },
    { id: 'audit', label: '审计中心', icon: 'audit', description: '查看请求审计、平台操作及关联记录。' },
  ] },
  { label: '技术接入与配置', items: [
    { id: 'capabilities', label: '识别与证据能力', icon: 'classification', description: '手动页面字段标注、文件发现、指纹溯源、分类知识与本地模型训练。' },
    { id: 'discovery', label: '多源自动发现', icon: 'integrations', description: '配置发现来源与任务，核对运行和识别规则。' },
    { id: 'automation', label: '自动采集', icon: 'automation', description: '配置采集来源与周期，查看运行及归档。' },
    { id: 'templates', label: '检查模板', icon: 'templates', description: '查看适用条件、取证要求和发布版本。' },
    { id: 'integrations', label: '集成与导入', icon: 'integrations', description: '查看实际接入能力、校验及导入清单。' },
  ] },
] as const

export type ToolPage = (typeof toolGroups)[number]['items'][number]
export const toolPages = toolGroups.flatMap<ToolPage>(group => group.items)

export const configurationToolGroups = [
  { label: '数据接入与采集', items: toolPages.filter(page => ['discovery', 'automation', 'integrations', 'capabilities'].includes(page.id)) },
  { label: '规则与模板配置', items: toolPages.filter(page => ['templates', 'runtime-policies'].includes(page.id)) },
]
