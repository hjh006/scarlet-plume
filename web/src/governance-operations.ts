import { validateAIAuthorizationExpiry } from './ai-security-editor.ts'

export const governanceKinds={rights_request:'个人权利请求',consent:'同意与撤回',vendor:'委托与供应商',audit:'审计与评估',incident:'事件报告',labeling:'生成内容标识'} as const
export type GovernanceKind=keyof typeof governanceKinds
export const governanceStatuses:Record<string,string>={draft:'待启动',in_progress:'办理中',in_review:'待独立复核',needs_work:'待补充',completed:'已完成复核',withdrawal_pending:'撤回传播中',withdrawn:'已复核撤回',cancelled:'已取消'}
export const governanceActionLabels:Record<string,string>={start:'开始办理',submit:'提交独立复核',approve:'通过独立复核',request_changes:'退回补充',cancel:'取消办理',withdraw:'启动同意撤回'}
export const receiptLabels:Record<string,string>={identity_verification:'身份核验',response_delivery:'答复送达',rights_execution:'权利请求源端执行',consent_grant:'同意取得',withdrawal_propagation:'撤回传播',contract:'合同依据',audit_report:'审计报告',initial_report:'事件初报',final_report:'事件总结',labeling_validation:'标识验证'}
export interface GovernanceRegulation {id:string;title:string;version:string;published:string;effective:string;status:string;sourceUrl:string;scope:string;boundary:string}
export interface GovernanceDeadline {code:string;title:string;regulationId?:string;dueAt?:string;status:string;unit?:string;amount?:number;receiptPresent:boolean;reason:string}
export interface GovernanceEvidence {id:number;checkId:number;kind:string;sourceSystem:string;sourceRecord:string;summary:string;observedAt:string;createdBy:number;provenance:string;snapshotDigest?:string}
export interface GovernanceHistory {id:number;action:string;note:string;actorId:number;actor:string;createdAt:string;revision:number;snapshotDigest:string}
export interface GovernanceReceipt {purpose:string;evidenceId:number;system?:string}
export interface GovernanceInput {revision?:number;kind:GovernanceKind;title:string;owner:string;activityId?:number;checkIds:number[];evidenceIds:number[];regulationIds:string[];applicability:string;applicabilityReason:string;details:Record<string,unknown>;receipts:GovernanceReceipt[]}
export interface GovernanceRecord extends GovernanceInput {id:number;revision:number;status:string;createdBy:number;updatedBy:number;createdAt:string;updatedAt:string;contributors:number[];withdrawing:boolean;regulations:GovernanceRegulation[];evidence:GovernanceEvidence[];evidenceIssues?:string[];deadlines:GovernanceDeadline[];history:GovernanceHistory[];historyNextCursor?:number;historyHasMore:boolean;boundary:string}
export interface GovernancePage<T> {items:T[];nextCursor?:number;hasMore:boolean}
export interface GovernanceCatalog {regulations:GovernanceRegulation[];kinds:string[];statuses:string[];boundary:string}
export interface GovernanceDraft {revision:number;kind:GovernanceKind;title:string;owner:string;activityId:string;checkIds:string;evidenceIds:string;regulationIds:string[];applicability:string;applicabilityReason:string;details:Record<string,string|boolean>;receipts:GovernanceReceipt[]}
interface Field {key:string;label:string;type?:'text'|'textarea'|'list'|'date'|'number'|'checkbox'|'select';required?:boolean;max?:number;options?:Record<string,string>;help?:string}
const statusOptions={unknown:'待核定',present:'已观察到',absent:'未观察到',not_applicable:'经判断不适用'}
export const governanceFields:Record<GovernanceKind,Field[]>={
  rights_request:[
    {key:'requestType',label:'请求事项',type:'select',options:{access:'查阅',copy:'复制',correct:'更正',delete:'删除',restrict:'限制处理',withdraw_consent:'撤回同意'},required:true},
    {key:'subjectRef',label:'申请主体 SHA-256 摘要',required:true,help:'仅填写 64 位小写十六进制摘要，不填姓名、手机号、证件号或原始用户标识。'},
    {key:'receivedAt',label:'实际接收时间（带时区）',type:'date',required:true},
    {key:'identityStatus',label:'身份核验状态',type:'select',options:{pending:'待核验',verified:'已核验',failed:'核验失败'},required:true},
    {key:'identityEvidenceId',label:'身份核验证据编号',type:'number',help:'引用当前工作区检查中的真实证据。'},
    {key:'responseDueAt',label:'内部承诺答复时间（可选）',type:'date',help:'人工设定的业务承诺，不表示统一法定答复期限。'},
    {key:'systems',label:'涉及处理系统',type:'list'},
    {key:'resolution',label:'请求办理结论',type:'select',options:{'':'尚未核定',fulfilled:'全部满足',partial:'部分满足',refused:'拒绝请求'},help:'删除、更正或限制处理实际完成时，逐系统关联源端执行证据；答复送达不表示源端操作已经完成。'},
    {key:'outcome',label:'处理结果与答复',type:'textarea'},
    {key:'retentionException',label:'删除或留存例外依据',type:'textarea',help:'保留须有适用依据；办理记录不自动执行源端删除。'},
  ],
  consent:[
    {key:'subjectRef',label:'主体 SHA-256 摘要',required:true,help:'不记录原始个人标识；摘要应在获准的客户侧生成。'},
    {key:'purpose',label:'明确处理目的',type:'textarea',required:true},
    {key:'lawfulBasis',label:'处理依据',required:true},
    {key:'noticeVersion',label:'告知文本版本',required:true},
    {key:'grantedAt',label:'实际取得同意时间（可选）',type:'date'},
    {key:'withdrawalAt',label:'实际收到撤回时间（可选）',type:'date'},
    {key:'systems',label:'需传播撤回的系统',type:'list',help:'每个系统分别绑定撤回传播证据；登记本身不停止源端处理。'},
  ],
  vendor:[
    {key:'name',label:'供应商或接收方名称',required:true},
    {key:'relationship',label:'处理关系',type:'select',options:{unknown:'待核定',entrusted:'受托处理',independent:'独立处理',joint:'共同处理'},required:true},
    {key:'processingScope',label:'处理目的与范围',type:'textarea',required:true},
    {key:'contractRef',label:'真实合同引用'},
    {key:'contractExpiresAt',label:'合同到期时间（可选）',type:'date'},
    {key:'subprocessors',label:'经确认的转委托方',type:'list'},
    {key:'crossBorder',label:'涉及数据跨境',type:'select',options:{unknown:'待核定',yes:'涉及',no:'不涉及'},required:true},
    {key:'reviewDueAt',label:'内部监督复核时间（可选）',type:'date'},
  ],
  audit:[
    {key:'scope',label:'审计或评估范围',type:'textarea',required:true},
    {key:'plannedAt',label:'内部审计计划截止（可选）',type:'date',help:'计划与实际完成分别登记，不用计划代替已完成报告。'},
    {key:'processorPeople',label:'处理自然人数（可选）',type:'number',max:10000000000,help:'经去重的自然人数，不能采用样本行数；0 也是明确的规模声明。'},
    {key:'scaleEvidenceId',label:'规模口径证据编号',type:'number'},
    {key:'auditor',label:'审计机构'},
    {key:'auditorGroup',label:'机构及关联方组标识'},
    {key:'leadAuditor',label:'项目负责人'},
    {key:'independenceNote',label:'独立性核对依据',type:'textarea'},
    {key:'previousAuditorGroups',label:'既往连续委托机构组',type:'list'},
    {key:'completedAt',label:'实际完成时间（可选）',type:'date'},
    {key:'reportRef',label:'报告引用'},
    {key:'minorData',label:'涉及未成年人个人信息',type:'checkbox',help:'特别规则与简化措施分别核定，不自动采用较长审计周期。'},
  ],
  incident:[
    {key:'level',label:'事件级别',type:'select',options:{unknown:'待核定',general:'一般',significant:'较大',major:'重大',severe:'特别重大'},required:true},
    {key:'operatorType',label:'运营者主体类型',type:'select',options:{unknown:'待核定',cii:'关键信息基础设施运营者',central:'中央国家机关及直属单位',other:'其他运营者'},required:true},
    {key:'detectedAt',label:'实际发现或获知时间（带时区）',type:'date',required:true},
    {key:'responseEndedAt',label:'实际处置结束时间（可选）',type:'date'},
    {key:'industryRule',label:'并行行业报告义务',type:'textarea'},
    {key:'channels',label:'经核定的实际报送渠道',type:'list',help:'每渠道分别引用初报、总结证据；引用存在不表示主管机关收件。'},
  ],
  labeling:[
    {key:'serviceScope',label:'实际服务范围',type:'select',options:{unknown:'待核定',internal:'内部使用',public:'向公众提供'},required:true},
    {key:'modality',label:'内容模态',type:'select',options:{text:'文本',image:'图片',audio:'音频',video:'视频',virtual_scene:'虚拟场景'},required:true},
    {key:'artifactDigest',label:'实际内容 SHA-256 摘要'},
    {key:'standardVersion',label:'采用标准及版本'},
    {key:'explicitStatus',label:'显式标识观察',type:'select',options:statusOptions,required:true},
    {key:'implicitStatus',label:'隐式标识观察',type:'select',options:statusOptions,required:true},
    {key:'exportStatus',label:'导出标识保留',type:'select',options:statusOptions,required:true},
    {key:'providerCode',label:'服务提供者编码'},
    {key:'contentId',label:'内容引用标识'},
    {key:'verificationMethod',label:'实际验证方法',type:'select',options:{manual:'人工观察',metadata:'元数据核对',unsupported:'当前格式未支持'},required:true,help:'人工或元数据观察不等于密码学签名验签，也不确认内容必由 AI 生成。'},
  ],
}
export const kindReceiptPurposes:Record<GovernanceKind,string[]>={rights_request:['identity_verification','response_delivery','rights_execution'],consent:['consent_grant','withdrawal_propagation'],vendor:['contract'],audit:['audit_report'],incident:['initial_report','final_report'],labeling:['labeling_validation']}

export function governanceDraft(kind:GovernanceKind='rights_request',record?:GovernanceRecord):GovernanceDraft {
  const details:Record<string,string|boolean>={}
  for(const field of governanceFields[kind]) {
    const value=record?.details[field.key]
    details[field.key]=field.type==='checkbox'?value===true:Array.isArray(value)?value.join('\n'):value===undefined || value===null?(field.type==='select'?Object.keys(field.options || {})[0] || '':''):String(value)
  }
  return {revision:record?.revision || 0,kind,title:record?.title || '',owner:record?.owner || '',activityId:record?.activityId?String(record.activityId):'',checkIds:(record?.checkIds || []).join(', '),evidenceIds:(record?.evidenceIds || []).join(', '),regulationIds:[...(record?.regulationIds || [])],applicability:record?.applicability || 'unknown',applicabilityReason:record?.applicabilityReason || '',details,receipts:(record?.receipts || []).map(item=>({...item}))}
}
export function governanceIDs(text:string):number[] {
  const values=text.split(/[\s,，]+/).filter(Boolean)
  if(values.length>50 || values.some(value=>!/^\d+$/.test(value) || !Number.isSafeInteger(Number(value)) || Number(value)<1))throw new Error('引用编号须为正整数，每类最多 50 个，用逗号或空格分隔。')
  return [...new Set(values.map(Number))]
}
export function governancePayload(draft:GovernanceDraft):GovernanceInput {
  const details:Record<string,unknown>={}
  for(const field of governanceFields[draft.kind]) {
    const value=draft.details[field.key]
    const text=typeof value==='string'?value.trim():''
    if(field.type==='checkbox') { details[field.key]=value===true;continue }
    if(field.required && !text)throw new Error('请填写'+field.label+'。')
    if(!text)continue
    if(field.type==='list') { details[field.key]=[...new Set(text.split(/\r?\n/).map(item=>item.trim()).filter(Boolean))];continue }
    if(field.type==='number') {
      if(!/^\d+$/.test(text) || !Number.isSafeInteger(Number(text)) || Number(text)>(field.max || Number.MAX_SAFE_INTEGER))throw new Error(field.label+'须为范围内非负整数。')
      details[field.key]=Number(text);continue
    }
    if(field.type==='date')validateAIAuthorizationExpiry(text)
    if(field.options && !(text in field.options))throw new Error('请核对'+field.label+'。')
    details[field.key]=text
  }
  for(const key of ['subjectRef','artifactDigest'])if(details[key] && !/^[a-f0-9]{64}$/.test(String(details[key])))throw new Error('主体与内容摘要须为 64 位小写 SHA-256；不要填写原始个人标识。')
  const evidenceIds=governanceIDs(draft.evidenceIds),checkIds=governanceIDs(draft.checkIds)
  const activity=draft.activityId.trim()
  if(activity && (!/^\d+$/.test(activity) || !Number.isSafeInteger(Number(activity)) || Number(activity)<1))throw new Error('活动编号须为正整数。')
  const receipts=draft.receipts.map(item=>({purpose:item.purpose,evidenceId:Number(item.evidenceId),...(item.system?.trim()?{system:item.system.trim()}: {})}))
  if(receipts.some(item=>!kindReceiptPurposes[draft.kind].includes(item.purpose) || !Number.isSafeInteger(item.evidenceId) || item.evidenceId<1 || !evidenceIds.includes(item.evidenceId)))throw new Error('每项用途回执都须引用已关联的真实证据编号。')
  return {...(draft.revision?{revision:draft.revision}:{}),kind:draft.kind,title:draft.title.trim(),owner:draft.owner.trim(),...(activity?{activityId:Number(activity)}:{}),checkIds,evidenceIds,regulationIds:[...draft.regulationIds],applicability:draft.applicability,applicabilityReason:draft.applicabilityReason.trim(),details,receipts}
}
export function governanceActions(record:GovernanceRecord,role:string):string[] {
  if(record.status==='in_review')return ['owner','reviewer'].includes(role)?['approve','request_changes']:[]
  if(!['owner','operator'].includes(role))return []
  if(record.kind==='consent' && record.status==='completed')return ['withdraw']
  if(record.status==='withdrawal_pending')return ['submit']
  if(record.status==='draft')return ['start','cancel']
  if(record.status==='needs_work')return ['start','submit','cancel']
  return record.status==='in_progress'?['submit','cancel']:[]
}
export function governanceCanEdit(record:GovernanceRecord,role:string):boolean {return ['owner','operator'].includes(role) && ['draft','in_progress','needs_work','withdrawal_pending'].includes(record.status)}
export function governanceSourceURL(value:string):string|undefined {try{const parsed=new URL(value);return parsed.protocol==='https:'?parsed.href:undefined}catch{return undefined}}
