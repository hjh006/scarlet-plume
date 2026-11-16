import { aiMaxModelWireBytes, aiText, type AIEditorSection, type AIRecord } from './ai-security-editor'

export interface AISecurityBundle {
  revision:number
  publishedRevision:number
  publishedDigest?:string
  configuration:AIRecord
  decisions:AIRecord[]
  evaluations:AIRecord[]
  approvals?:AIRecord[]
  coverage?:unknown
  updatedAt?:string
}
export const aiSections:AIEditorSection[] = [
  {key:'applications',label:'AI 应用',description:'限定应用可采用的主体、资料、模型和工具。应用接入与客户侧实际执行分别验收。',fields:[
    {key:'id',label:'应用标识',required:true},{key:'name',label:'应用名称',required:true},{key:'enabled',label:'启用应用',type:'checkbox'},
    {key:'principalIds',label:'允许主体标识',type:'list',reference:'principals'},{key:'documentIds',label:'允许资料标识',type:'list',reference:'documents'},{key:'modelIds',label:'允许模型目标标识',type:'list',reference:'models'},{key:'toolIds',label:'允许工具标识',type:'list',reference:'tools'},
    {key:'requireTask',label:'执行必须绑定有效任务',type:'checkbox',optional:true,help:'启用后，模型、资料和工具执行都需要与可信任务范围绑定；任务选择符不是调用凭证。网关应用的任务要求也会独立核对。'},
  ]},
  {key:'principals',label:'调用主体',description:'用户或工作负载通过客户侧凭证映射到主体。请求自报身份不会取代可信绑定。',fields:[
    {key:'id',label:'主体标识',required:true},{key:'name',label:'显示名称',required:true},{key:'enabled',label:'启用主体',type:'checkbox'},{key:'applicationIds',label:'允许使用的应用标识',type:'list',reference:'applications'},
    {key:'authorizationExpiresAt',label:'主体授权租约到期（可选）',type:'rfc3339',optional:true,maxLength:40,help:'填写带时区的 RFC3339 时间，如 2026-11-30T23:59:59+08:00。到期即拒绝；启用强制租约后，未声明也会拒绝。此租约由可信管理员维护，不代表外部身份系统已自动同步撤权。'},
  ]},
  {key:'documents',label:'知识资料与 ACL',description:'配置资料元数据和权限版本，实际正文保留在客户侧文档存储；配置登记本身不证明源 ACL 已被自动接入。',fields:[
    {key:'id',label:'资料标识',required:true},{key:'name',label:'资料名称',required:true},{key:'version',label:'来源版本',required:true},{key:'aclVersion',label:'权限版本',required:true},{key:'knowledgeBaseId',label:'知识库引用',required:true},{key:'sourceRef',label:'原始来源引用',required:true},
    {key:'integrityDigest',label:'RAG 正文 SHA-256 固定摘要（可选）',maxLength:64,help:'64 位小写十六进制 SHA-256，客户侧检索时按实际正文核对。未配置时仅核对来源版本与 ACL，不表示正文完整性已验证。'},
    {key:'authorizationExpiresAt',label:'资料授权租约到期（可选）',type:'rfc3339',optional:true,maxLength:40,help:'填写带时区的 RFC3339 时间，如 2026-11-30T23:59:59+08:00。资料授权到期即拒绝；共同策略强制租约后，未声明的资料也会拒绝。过期配置可保存用于验证拒绝行为。'},
    {key:'allowedPrincipalIds',label:'允许读取的主体标识',type:'list',reference:'principals'},{key:'categories',label:'数据类别',type:'list'},{key:'enabled',label:'启用资料',type:'checkbox'},
    {key:'usePolicy',label:'资料用途、留存与训练限制（可选）',type:'json',optional:true,help:'示例：{"allowedPurposes":["support-answer"],"maxRetentionHours":0,"allowTraining":false}。用途由可信任务记录派生；0 小时要求零留存，空用途列表不允许任何用途。留空可移除此扩展。'},
  ]},
  {key:'models',label:'模型目标',description:'端点引用绑定客户侧独立维护的固定 HTTPS 目标，正文中不能自选上游。地域及供应商信息需有确认依据。',fields:[
    {key:'id',label:'模型目标标识',required:true},{key:'name',label:'显示名称',required:true},{key:'provider',label:'模型供应商',required:true},{key:'upstreamModel',label:'受信任上游模型／部署标识',maxLength:160,help:'留空时采用逻辑模型标识。客户侧将此值注入固定上游，输入 JSON 的 model 选择必须一致。'},{key:'endpointRef',label:'固定端点引用',required:true},{key:'region',label:'声明处理地域',required:true},{key:'allowedApplicationIds',label:'允许调用的应用标识',type:'list',reference:'applications'},{key:'allowedInputCategories',label:'此模型允许输入的数据类别',type:'list',help:'同时满足共同策略和本模型类别。当前输入类别会保守约束输出，两处均需明确允许。'},{key:'allowedOutputCategories',label:'此模型允许输出的数据类别',type:'list'},{key:'enabled',label:'启用模型目标',type:'checkbox'},
    {key:'responseFormat',label:'上游响应校验格式（可选）',type:'select',optional:true,options:[{value:'',label:'默认 JSON'},{value:'json',label:'JSON（显式）'},{value:'openai_chat',label:'严格 OpenAI Chat 文本响应'}],help:'严格格式核对实际响应模型与固定映射，拒绝工具调用并规范化后检查输出。流式请求采用 strict_hold 完整检测后回放；媒体范围由下方明确配置及实际扫描能力决定，不代表任意供应商协议均可用。'},
    {key:'media',label:'多模态输入与检测配置（可选）',type:'media',optional:true,help:'仅支持 PNG、JPEG 的实际 OCR，以及 PCM16 单声道 16 kHz、最长 30 秒 WAV 的实际 ASR。输入强制标记 image 或 audio，未识别出文本时增加 unclassified_media；共同策略与模型输入类别必须分别明确允许。检测失败或扫描器未就绪时拒绝，配置不等于检测已生效。'},
    {key:'handling',label:'目的端留存与训练约定（可选）',type:'json',optional:true,help:'示例：{"retentionHours":0,"trainingUse":"prohibited","evidenceRef":"contract-v1","authorizationExpiresAt":"2026-11-30T23:59:59+08:00"}。这是经批准且有期限的供应商处理声明，不独立证明远端实际行为。留空移除此扩展。'},
  ]},
  {key:'tools',label:'Agent 工具',description:'限定工具动作、目标和参数。普通 JSON 与 MCP HTTP 分别配置；高影响操作须采用与当前请求绑定的独立批准。',fields:[
    {key:'id',label:'工具标识',required:true},{key:'name',label:'工具名称',required:true},{key:'endpointRef',label:'固定执行端点引用',required:true},
    {key:'protocol',label:'上游工具协议',type:'select',initial:'json',required:true,options:[{value:'json',label:'普通 HTTPS JSON'},{value:'mcp_http',label:'MCP HTTP · 2026-07-28'}],help:'旧配置未填时采用普通 JSON。MCP 采用无会话 tools/call，支持有界 JSON 与 SSE 汇聚；最终响应核对后返回，不透传、自动重连或重试。'},
    {key:'protocolVersion',label:'MCP 协议版本固定（可选）',type:'select',optional:true,options:[{value:'',label:'未显式固定（MCP 默认 2026-07-28）'},{value:'2026-07-28',label:'2026-07-28'}],help:'仅 MCP 工具使用；普通 JSON 留空。当前仅支持此版本，不采用调用者自报协议版本。'},
    {key:'definitionDigests',label:'MCP 工具定义摘要固定（可选）',type:'json',optional:true,help:'JSON 对象：每个允许动作对应 64 位小写 SHA-256，由可信审核流程对单个完整工具定义规范化后生成，不能直接采用原始 JSON 文本哈希。非空须覆盖全部动作且无额外动作；执行前以单页 tools/list 核对。留空或 {} 表示未固定定义。'},
    {key:'targetArgument',label:'MCP 可信目标参数名',maxLength:80,help:'仅 MCP 必填，如 resourceId。运行时将已授权的请求目标注入此参数；普通 JSON 请留空。不得在下方参数约束中重复登记。'},
    {key:'allowedApplicationIds',label:'允许应用标识',type:'list',reference:'applications'},{key:'allowedPrincipalIds',label:'允许主体标识',type:'list',reference:'principals'},{key:'actions',label:'允许动作／MCP 工具名',type:'list',help:'MCP 工具将已授权动作映射为 tools/call 的 name，不采用调用参数自报的工具名。'},{key:'targets',label:'允许目标',type:'lines',help:'每行一个精确目标，内容中的逗号按原文保留。'},
    {key:'allowedArguments',label:'每个参数允许的值',type:'json',help:'JSON 对象，键为参数名，值为允许值的字符串数组。示例：{"orderId":["A100"]}。'},
    {key:'requiredArguments',label:'必填参数名',type:'list'},{key:'highImpact',label:'高影响动作，需要独立批准',type:'checkbox'},{key:'enabled',label:'启用工具',type:'checkbox'},
    {key:'allowedInputCategories',label:'工具允许接收的数据类别（可选）',type:'list',optional:true,help:'任务绑定的工具必须明确允许任务累计的数据类别；空列表不会放行已标记的敏感数据。'},
    {key:'handling',label:'工具接收数据的处理约定（可选）',type:'json',optional:true,help:'JSON 字段 retentionHours、trainingUse、evidenceRef、authorizationExpiresAt 与模型一致。经批准的约定到期即拒绝相应数据外发；不代表远端实际行为已被独立验证。'},
  ]},
  {key:'policy',label:'共同数据与检测策略',description:'对所有已登记请求采用明确的大小、时效、数据类别、地域及规则限制。空允许列表不代表全放行。',fields:[
    {key:'version',label:'策略版本',required:true},{key:'maxRequestAgeSeconds',label:'请求最大有效时长（秒）',type:'number',required:true,max:3600},{key:'maxInputBytes',label:'输入最大字节数',type:'number',required:true,max:aiMaxModelWireBytes,help:'模型 wire 最多 16 MiB，用于受控媒体 JSON。文本／JSON 检测另有独立预算，检查超限仍拒绝；不表示 16 MiB 普通文本全兼容。媒体单体与总量仍按模型 media 配置限制。'},{key:'maxOutputBytes',label:'输出最大字节数',type:'number',required:true,max:aiMaxModelWireBytes,help:'模型 wire 最多 16 MiB；媒体扫描和文本／JSON 检测保留各自独立预算，超限或未完成检查时不释放。'},
    {key:'requiredRuntimeProtocolVersion',label:'要求的运行时协议版本（可选）',type:'select',optional:true,options:[{value:'',label:'未固定运行时版本'},{value:'scarlet-ai-runtime-v2',label:'scarlet-ai-runtime-v2'}],help:'固定后由真实客户侧运行时核对版本；控制台请求不能自报版本来获得授权。'},
    {key:'requiredInspectionVersion',label:'要求的检测器版本（可选）',type:'select',optional:true,options:[{value:'',label:'未固定检测器版本'},{value:'datasafety-four-format-v1',label:'datasafety-four-format-v1'}],help:'固定后要求采用指定四类格式检测器。版本固定不扩大已声明检测范围。'},
    {key:'requireAuthorizationLeases',label:'强制主体与资料具备有效授权租约',type:'checkbox',optional:true,help:'启用后，主体或访问资料缺少租约即拒绝；已声明租约无论本项是否启用，到期都会拒绝。请先补齐可信租约再验收发布。'},
    {key:'requirePinnedSourceContent',label:'资料必须固定实际正文摘要',type:'checkbox',optional:true,help:'启用后要求资料具备受信任正文摘要，实际检索时核对正文、来源版本与 ACL。仅登记文档 ID 不证明正文来源。'},
    {key:'requireDataHandling',label:'外发必须具备有效处理约定',type:'checkbox',optional:true,help:'启用后，模型或工具接收数据前核对留存、训练用途、证据引用与有效期；声明不会替代真实目的端行为验收。'},
    {key:'allowedInputCategories',label:'允许输入及外发的数据类别',type:'list'},{key:'allowedOutputCategories',label:'允许输出的数据类别',type:'list'},{key:'allowedModelRegions',label:'允许模型处理地域',type:'list'},{key:'blockedInputPatterns',label:'输入拒绝正则规则',type:'lines',help:'每行一条 Go 正则表达式，保留量词中的逗号。覆盖范围以采用版本和实际回归用例为准。'},{key:'blockedOutputPatterns',label:'输出拒绝正则规则',type:'lines'},
  ]},
]
for (const section of aiSections) {
  if (['applications','models','tools'].includes(section.key)) section.fields.push(
    {key:'version',label:'组件版本（来源声明）'},
    {key:'sourceRef',label:'原始来源引用（来源声明）'},
    {key:'integrityDigest',label:'完整性摘要（来源声明）',help:'填写来源确认的摘要；登记本字段不代表已完成签名或供应链验证。'},
  )
}
export function aiRecords(configuration:AIRecord,key:string):AIRecord[] {
  const value=configuration[key]
  if(key==='policy') return [typeof value==='object' && value!==null && !Array.isArray(value) ? value as AIRecord : {}]
  return Array.isArray(value) ? value.filter((item):item is AIRecord=>typeof item==='object' && item!==null && !Array.isArray(item)) : []
}
export function aiString(item:AIRecord|undefined,...keys:string[]) {
  for (const key of keys) {
    if(item?.[key]===undefined || item[key]===null) continue
    const text=aiText(item[key])
    if(text!=='') return text
  }
  return ''
}
export function aiDecision(item:AIRecord):AIRecord {
  return typeof item.decision==='object' && item.decision!==null && !Array.isArray(item.decision) ? item.decision as AIRecord : item
}
export function aiDecisionLabel(item:AIRecord):string {
  const value=aiDecision(item)
  if(value.allowed===true || value.allow===true) return '允许'
  if(value.allowed===false || value.allow===false) return '拒绝'
  const action=aiString(value,'outcome','status','result','action')
  return ({allow:'允许',deny:'拒绝',blocked:'已阻止',rejected:'已拒绝',approved:'已批准',pending:'待批准',pending_review:'待独立复核',proposed:'待独立批准',invalidated:'已失效',approval_required:'需要批准',unknown:'未知',passed:'通过',failed:'未通过'} as Record<string,string>)[action] || action || '待核对'
}
export function aiReason(item:AIRecord):string {
  const value=aiDecision(item)
  return aiString(value,'reason','message','code') || aiText(value.reasons) || '请展开原始决策核对依据。'
}
