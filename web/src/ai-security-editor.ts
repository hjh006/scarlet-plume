export type AIRecord = Record<string, unknown>
export interface AIEditorField {
  key: string
  label: string
  type?: 'text' | 'textarea' | 'list' | 'lines' | 'number' | 'checkbox' | 'json' | 'select' | 'datetime' | 'rfc3339' | 'media'
  required?: boolean
  help?: string
  options?: { value:string; label:string }[]
  initial?: unknown
  reference?: string
  max?: number
  maxLength?: number
  optional?: boolean
}
export interface AIEditorSection { key:string; label:string; description:string; fields:AIEditorField[] }

export function aiList(value:string):string[] {
  return [...new Set(value.split(/[,，\n]+/).map(item => item.trim()).filter(Boolean))]
}
export function aiText(value:unknown):string {
  if (Array.isArray(value)) return value.map(String).join(', ')
  if (value === null || value === undefined) return ''
  if (typeof value === 'object') return JSON.stringify(value,null,2)
  return String(value)
}
export function aiFieldValue(field:AIEditorField,value:string|boolean):unknown {
  if (field.type === 'checkbox') return Boolean(value)
  if (field.type === 'rfc3339') return String(value).trim() || null
  if (field.type === 'list') return aiList(String(value))
  if (field.type === 'lines') return [...new Set(String(value).split(/\r?\n/).filter(item=>item.length>0))]
  if (field.type === 'number') {
    const parsed = Number(value)
    if (!Number.isFinite(parsed)) throw new Error('请填写有效数字。')
    return parsed
  }
  if (field.type === 'json') {
    if(field.optional && !String(value).trim())return null
    const parsed:unknown = JSON.parse(String(value) || '{}')
    if(field.optional && parsed===null)return null
    if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) throw new Error('请填写 JSON 对象。')
    return parsed
  }
  return String(value)
}
export function aiRecordDraft(section:AIEditorSection):AIRecord {
  return Object.fromEntries(section.fields.filter(field=>!field.optional || field.initial!==undefined).map(field => [field.key,field.initial ?? (['list','lines'].includes(field.type || '') ? [] : field.type==='checkbox' ? false : field.type==='json' ? {} : field.type==='number' ? 0 : '')]))
}
export function aiRecordLabel(item:AIRecord,index:number):string {
  return aiText(item.name || item.id || item.subject || item.subjectId || item.tool || item.toolId) || `未命名记录 ${index+1}`
}
export function aiConfigHasDuplicateIds(config:Record<string,unknown>,sections:AIEditorSection[]):string[] {
  const errors:string[] = []
  for (const section of sections) {
    const records = config[section.key]
    if (!Array.isArray(records)) continue
    const seen = new Set<string>()
    for (const item of records) {
      if (typeof item !== 'object' || !item) continue
      const id = aiText((item as AIRecord).id).trim()
      if (id && seen.has(id)) errors.push(`${section.label}存在重复标识：${id}`)
      seen.add(id)
    }
  }
  return errors
}

export interface AIRequestDraft {
  requestId:string
  taskId?:string
  kind:'rag'|'model'|'tool'
  applicationId:string
  principalId:string
  documentIds:string[]
  modelId:string
  toolId:string
  action:string
  target:string
  argumentsJSON:string
  input:string
  output:string
  approvalId:string
  issuedAt:string
  expiresAt:string
}
export function newAIRequestId():string {
  if(typeof globalThis.crypto.randomUUID==='function') return globalThis.crypto.randomUUID()
  return Array.from(globalThis.crypto.getRandomValues(new Uint8Array(16)),value=>value.toString(16).padStart(2,'0')).join('')
}
export function buildAIRequest(draft:AIRequestDraft):AIRecord {
  const base:AIRecord={requestId:draft.requestId,kind:draft.kind,applicationId:draft.applicationId,principalId:draft.principalId,issuedAt:draft.issuedAt,expiresAt:draft.expiresAt}
  if(draft.taskId?.trim()) {
    if(!/^[A-Za-z0-9][A-Za-z0-9_.:-]{0,79}$/.test(draft.taskId.trim()))throw new Error('任务标识需要明确有效 ID，不能包含请求正文。')
    base.taskId=draft.taskId.trim()
  }
  if(draft.kind==='rag') return {...base,documentIds:[...draft.documentIds]}
  if(draft.kind==='model') return {...base,modelId:draft.modelId,documentIds:[...draft.documentIds],input:draft.input,output:draft.output}
  const args:unknown=JSON.parse(draft.argumentsJSON || '{}')
  if(!args || typeof args!=='object' || Array.isArray(args) || Object.values(args).some(value=>typeof value!=='string')) throw new Error('工具参数应是所有值均为字符串的 JSON 对象。')
  return {...base,toolId:draft.toolId,action:draft.action,target:draft.target,arguments:args,approvalId:draft.approvalId}
}
export function buildAIWireRequest(draft:AIRequestDraft):AIRecord {
  const {applicationId: _applicationId,principalId: _principalId,output: _output,...wire}=buildAIRequest(draft)
  return wire
}
export function validateAIToolProtocol(tool:AIRecord):void {
  const protocol=tool.protocol ?? ''
  if(typeof protocol!=='string' || !['','json','mcp_http'].includes(protocol)) throw new Error('工具协议仅支持普通 JSON 或 MCP HTTP 2026-07-28。')
  const version=tool.protocolVersion ?? ''
  if(typeof version!=='string') throw new Error('MCP 协议版本应为 2026-07-28 或留空。')
  const definitions=tool.definitionDigests ?? {}
  if(!definitions || typeof definitions!=='object' || Array.isArray(definitions)) throw new Error('MCP 定义摘要应是动作名到 SHA-256 的 JSON 对象。')
  const entries=Object.entries(definitions)
  if(protocol!=='mcp_http') {
    if((tool.targetArgument ?? '')!=='' || version!=='' || entries.length) throw new Error('可信目标参数、MCP 版本和定义摘要仅用于 MCP HTTP 工具；普通 JSON 工具请留空。')
    return
  }
  if(version!=='' && version!=='2026-07-28') throw new Error('当前 MCP 仅支持 2026-07-28 协议版本。')
  const actions=Array.isArray(tool.actions)?tool.actions:[]
  if(entries.length && (entries.length!==actions.length || actions.some(action=>typeof action!=='string' || !Object.prototype.hasOwnProperty.call(definitions,action)) || entries.some(([action,value])=>!actions.includes(action) || typeof value!=='string' || !/^[0-9a-f]{64}$/.test(value)))) throw new Error('MCP 定义摘要非空时必须覆盖全部允许动作且没有额外动作；每项为 64 位小写十六进制 SHA-256。')
  if(typeof tool.targetArgument!=='string' || !/^[A-Za-z0-9][A-Za-z0-9_.:-]{0,79}$/.test(tool.targetArgument)) throw new Error('MCP 工具需要有效的可信目标参数名：1—80 位字母、数字或 _ . : -，以字母或数字开头。')
  const parameters=tool.allowedArguments
  if((parameters && typeof parameters==='object' && Object.prototype.hasOwnProperty.call(parameters,tool.targetArgument)) || (Array.isArray(tool.requiredArguments) && tool.requiredArguments.includes(tool.targetArgument))) throw new Error('MCP 可信目标参数由运行时注入，不能同时登记为调用者允许或必填参数。')
}
export function validateAIAuthorizationExpiry(value:unknown):void {
  if(value===undefined || value===null) return
  const parts=typeof value==='string'?/^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d{1,9}))?(Z|[+-](\d{2}):(\d{2}))$/.exec(value):null
  if(!parts) throw new Error('授权租约应填写带时区的 RFC3339 时间，如 2026-11-30T23:59:59+08:00；不设置时请清空。')
  const year=Number(parts[1]),month=Number(parts[2]),day=Number(parts[3])
  const days=[31,year%4===0 && (year%100!==0 || year%400===0)?29:28,31,30,31,30,31,31,30,31,30,31]
  const time=Date.parse(String(value))
  if(month<1 || month>12 || day<1 || day>days[month-1]! || Number(parts[4])>23 || Number(parts[5])>59 || Number(parts[6])>59 || Number(parts[9] || 0)>23 || Number(parts[10] || 0)>59 || !Number.isFinite(time) || (time===-62135596800000 && !/[1-9]/.test(parts[7] || ''))) throw new Error('授权租约时间无效，不能采用零时间。')
}
export const aiMediaMimeTypes=['image/png','image/jpeg','audio/wav']
export function aiMediaDraft(value:unknown):AIRecord {
  const media=value && typeof value==='object' && !Array.isArray(value)?value as AIRecord:{}
  return {allowedMimeTypes:Array.isArray(media.allowedMimeTypes)?[...media.allowedMimeTypes]:[],maxAssetBytes:media.maxAssetBytes ?? 4194304,maxTotalBytes:media.maxTotalBytes ?? 8388608,maxPixels:media.maxPixels ?? 16000000,inspectionVersion:media.inspectionVersion ?? 'scarlet-media-ocr-asr-v1'}
}
export function validateAIMedia(value:unknown):void {
  if(value===undefined || value===null)return
  if(typeof value!=='object' || Array.isArray(value))throw new Error('多模态配置应为明确的检测配置对象。')
  const media=value as AIRecord
  if(Object.keys(media).some(key=>!['allowedMimeTypes','maxAssetBytes','maxTotalBytes','maxPixels','inspectionVersion'].includes(key)))throw new Error('多模态配置包含未受支持的字段。')
  if(!Array.isArray(media.allowedMimeTypes) || !media.allowedMimeTypes.length || media.allowedMimeTypes.length>3 || new Set(media.allowedMimeTypes).size!==media.allowedMimeTypes.length || media.allowedMimeTypes.some(type=>!aiMediaMimeTypes.includes(String(type))))throw new Error('多模态 MIME 仅支持 image/png、image/jpeg、audio/wav，并需要明确选择。')
  for(const [key,maximum] of [['maxAssetBytes',4194304],['maxTotalBytes',8388608],['maxPixels',16000000]] as const)if(!Number.isSafeInteger(media[key]) || Number(media[key])<1 || Number(media[key])>maximum)throw new Error('多模态限制应为正整数：单体最多 4 MiB、总量最多 8 MiB、像素最多 1600 万。')
  if(Number(media.maxTotalBytes)<Number(media.maxAssetBytes))throw new Error('请求媒体总量限制不能小于单体限制。')
  if(media.inspectionVersion!=='scarlet-media-ocr-asr-v1')throw new Error('多模态检测器版本仅支持 scarlet-media-ocr-asr-v1。')
}
export const aiMaxModelWireBytes=16777216
const aiScopeIdentifier=/^[A-Za-z0-9][A-Za-z0-9_.:-]{0,79}$/
export function validateAIDataUse(value:unknown):void {
  if(value===undefined || value===null)return
  if(typeof value!=='object' || Array.isArray(value))throw new Error('资料使用限制需要 JSON 对象。')
  const policy=value as AIRecord
  if(Object.keys(policy).some(key=>!['allowedPurposes','maxRetentionHours','allowTraining'].includes(key)) || !Array.isArray(policy.allowedPurposes) || policy.allowedPurposes.length>64 || new Set(policy.allowedPurposes).size!==policy.allowedPurposes.length || policy.allowedPurposes.some(purpose=>typeof purpose!=='string' || !aiScopeIdentifier.test(purpose)))throw new Error('资料用途需要最多 64 个唯一明确标识，不接受调用方声明或额外字段。')
  if(!Number.isSafeInteger(policy.maxRetentionHours) || Number(policy.maxRetentionHours)<0 || Number(policy.maxRetentionHours)>8760 || typeof policy.allowTraining!=='boolean')throw new Error('资料保留期限应为 0—8760 小时的整数，并明确是否允许训练；0 表示要求零留存。')
}
export function validateAIDataHandling(value:unknown):void {
  if(value===undefined || value===null)return
  if(typeof value!=='object' || Array.isArray(value))throw new Error('目的端处理约定需要 JSON 对象。')
  const handling=value as AIRecord
  if(Object.keys(handling).some(key=>!['retentionHours','trainingUse','evidenceRef','authorizationExpiresAt'].includes(key)) || !Number.isSafeInteger(handling.retentionHours) || Number(handling.retentionHours)<0 || Number(handling.retentionHours)>8760 || !['prohibited','permitted'].includes(String(handling.trainingUse)) || typeof handling.evidenceRef!=='string' || !aiScopeIdentifier.test(handling.evidenceRef) || typeof handling.authorizationExpiresAt!=='string')throw new Error('目的端约定需要 0—8760 小时留存、prohibited/permitted 训练用途、明确证据引用和带时区的到期时间。')
  validateAIAuthorizationExpiry(handling.authorizationExpiresAt)
}
export function validateAIConfigurationExtensions(configuration:AIRecord):void {
  const records=(key:string)=>(Array.isArray(configuration[key])?configuration[key]:[]) as AIRecord[]
  for(const key of ['principals','documents']) for(const item of records(key)) {
    if(item && typeof item==='object') validateAIAuthorizationExpiry(item.authorizationExpiresAt)
  }
  for(const model of records('models')) if(model && typeof model==='object' && !['','json','openai_chat'].includes(String(model.responseFormat ?? ''))) throw new Error('模型响应格式仅支持默认 JSON 或严格 OpenAI Chat 文本响应。')
  for(const model of records('models'))if(model && typeof model==='object')validateAIMedia(model.media)
  for(const doc of records('documents'))if(doc && typeof doc==='object')validateAIDataUse(doc.usePolicy)
  for(const kind of ['models','tools'])for(const destination of records(kind))if(destination && typeof destination==='object')validateAIDataHandling(destination.handling)
  for(const app of records('applications'))if(app && typeof app==='object' && app.requireTask!==undefined && typeof app.requireTask!=='boolean')throw new Error('应用强制任务约束应为布尔值。')
  for(const tool of records('tools')) if(tool && typeof tool==='object') validateAIToolProtocol(tool)
  const policy=configuration.policy && typeof configuration.policy==='object'?configuration.policy as AIRecord:{}
  for(const key of ['maxInputBytes','maxOutputBytes'])if(policy[key]!==undefined && (!Number.isSafeInteger(policy[key]) || Number(policy[key])<1 || Number(policy[key])>aiMaxModelWireBytes))throw new Error('输入／输出 wire 上限应为 1—16777216 字节的整数；文本和 JSON 检测仍受检测器独立预算限制。')
  if(!['','scarlet-ai-runtime-v2'].includes(String(policy.requiredRuntimeProtocolVersion ?? ''))) throw new Error('要求的运行时协议版本仅支持 scarlet-ai-runtime-v2 或留空。')
  if(!['','datasafety-four-format-v1'].includes(String(policy.requiredInspectionVersion ?? ''))) throw new Error('要求的检测器版本仅支持 datasafety-four-format-v1 或留空。')
  if(policy.requireAuthorizationLeases!==undefined && policy.requireAuthorizationLeases!==null && typeof policy.requireAuthorizationLeases!=='boolean') throw new Error('强制授权租约应为布尔值。')
  for(const key of ['requirePinnedSourceContent','requireDataHandling'])if(policy[key]!==undefined && policy[key]!==null && typeof policy[key]!=='boolean')throw new Error('资料正文固定与目的端处理约定要求应为布尔值。')
}
export function validateAIToolRequest(tool:AIRecord|undefined,args:unknown):void {
  if(!tool) return
  validateAIToolProtocol(tool)
  if(tool.protocol==='mcp_http' && args && typeof args==='object' && Object.prototype.hasOwnProperty.call(args,String(tool.targetArgument))) throw new Error('MCP 可信目标参数由请求目标注入，请从参数 JSON 中移除此字段。')
}
export function validateAIModelPayload(input:string,upstreamModel:string,mediaPolicy?:unknown):void {
  const value:unknown=JSON.parse(input)
  if(!value || typeof value!=='object' || Array.isArray(value)) throw new Error('模型执行输入应为受支持的 JSON 对象。')
  const payload=value as AIRecord
  const supported=['model','prompt','input','messages','stream','stream_options','max_tokens','temperature','modalities','audio']
  const extra=Object.keys(payload).filter(key=>!supported.includes(key))
  if(extra.length) throw new Error('当前模型适配不支持这些字段：'+extra.join('、'))
  if(payload.model!==undefined && payload.model!==null && typeof payload.model!=='string') throw new Error('model 应为模型标识字符串。')
  if(payload.model && upstreamModel && payload.model!==upstreamModel) throw new Error('输入 JSON 的 model 与受信任上游模型／部署不一致。')
  if(payload.stream!==undefined && payload.stream!==null && typeof payload.stream!=='boolean') throw new Error('stream 应为布尔值；true 采用 strict_hold 完整检测后回放。')
  const object=(value:unknown):value is AIRecord=>!!value && typeof value==='object' && !Array.isArray(value)
  if(payload.stream_options!==undefined && payload.stream_options!==null) {
    if(payload.stream!==true || !object(payload.stream_options) || Object.keys(payload.stream_options).some(key=>key!=='include_usage') || (payload.stream_options.include_usage!==undefined && typeof payload.stream_options.include_usage!=='boolean'))throw new Error('stream_options 仅支持流式请求的 include_usage 布尔值。')
  }
  for(const key of ['prompt','input']) if(payload[key]!==undefined && payload[key]!==null && typeof payload[key]!=='string') throw new Error(key+' 仅支持文本字符串。')
  if(payload.messages!==undefined && payload.messages!==null && !Array.isArray(payload.messages)) throw new Error('messages 应为消息数组。')
  const media=object(mediaPolicy)?mediaPolicy:undefined
  if(media)validateAIMedia(media)
  const messages=Array.isArray(payload.messages)?payload.messages:[]
  if(messages.length>128) throw new Error('messages 不能超过 128 条。')
  let assetCount=0,totalBytes=0
  function asset(mime:string,encoded:unknown):void {
    if(!media || !(media.allowedMimeTypes as string[]).includes(mime))throw new Error('媒体类型需要在已保存模型的 media 配置中明确批准。')
    if(typeof encoded!=='string' || !encoded || encoded.length%4!==0 || encoded.length>Math.ceil(Number(media.maxAssetBytes)/3)*4 || !/^[A-Za-z0-9+/]*={0,2}$/.test(encoded))throw new Error('媒体内容需要有效的标准 Base64，不支持远程 URL 或扫描结果声明。')
    const bytes=encoded.length/4*3-(encoded.endsWith('==')?2:encoded.endsWith('=')?1:0)
    totalBytes+=bytes;assetCount++
    if(bytes>Number(media.maxAssetBytes) || totalBytes>Number(media.maxTotalBytes) || assetCount>16)throw new Error('媒体内容超过模型声明的单体、总量或 16 个媒体资产限制。')
  }
  for(const message of messages) {
    if(!object(message) || Object.keys(message).some(key=>!['role','content'].includes(key)) || !['system','user','assistant'].includes(String(message.role)))throw new Error('每条消息只支持 system/user/assistant 的 role/content，不支持工具或额外声明。')
    if(typeof message.content==='string')continue
    if(!media || !Array.isArray(message.content) || !message.content.length || message.content.length>16)throw new Error('媒体消息需要已保存的模型 media 授权，每条消息最多 16 个内容片段。')
    for(const part of message.content) {
      if(!object(part) || Object.keys(part).some(key=>!['type','text','image_url','input_audio'].includes(key)))throw new Error('内容片段包含未受支持的字段；不能自报分类、投影或扫描结果。')
      if(part.type==='text' && typeof part.text==='string' && !part.image_url && !part.input_audio)continue
      if(part.type==='image_url' && object(part.image_url) && !part.input_audio && !part.text && Object.keys(part.image_url).every(key=>['url','detail'].includes(key))) {
        if(part.image_url.detail!==undefined && !['','auto','low','high'].includes(String(part.image_url.detail)))throw new Error('图片 detail 仅支持 auto、low 或 high。')
        const uri=typeof part.image_url.url==='string'?/^data:(image\/(?:png|jpeg));base64,(.*)$/.exec(part.image_url.url):null
        if(!uri)throw new Error('图片仅支持 PNG/JPEG 的内联 Base64 data URI；远程媒体 URL 不会被抓取。')
        asset(uri[1]!,uri[2]);continue
      }
      if(part.type==='input_audio' && object(part.input_audio) && !part.image_url && !part.text && Object.keys(part.input_audio).every(key=>['data','format'].includes(key)) && part.input_audio.format==='wav') {asset('audio/wav',part.input_audio.data);continue}
      throw new Error('内容片段仅支持 text、image_url 或 input_audio wav。实际格式、像素、音频及扫描结果由运行时核对。')
    }
  }
  if([typeof payload.prompt==='string',typeof payload.input==='string',messages.length>0].filter(Boolean).length!==1) throw new Error('prompt、input、messages 需要且只能采用一种输入。')
  if(payload.max_tokens!==undefined && payload.max_tokens!==null && (!Number.isInteger(payload.max_tokens) || Number(payload.max_tokens)<0 || Number(payload.max_tokens)>4096)) throw new Error('max_tokens 应为 0—4096 的整数。')
  if(payload.temperature!==undefined && payload.temperature!==null && (typeof payload.temperature!=='number' || payload.temperature<0 || payload.temperature>2)) throw new Error('temperature 应在 0—2 之间。')
  const modalities=payload.modalities
  if(modalities!==undefined && modalities!==null && (!media || !Array.isArray(modalities) || modalities.length>2 || new Set(modalities).size!==modalities.length || !modalities.includes('text') || modalities.some(value=>!['text','audio'].includes(String(value)))))throw new Error('输出 modalities 需要模型媒体授权，且只支持 text 或 text/audio。')
  const audioRequested=Array.isArray(modalities) && modalities.includes('audio')
  if(audioRequested && (payload.stream===true || !media || !(media.allowedMimeTypes as string[]).includes('audio/wav') || !object(payload.audio) || Object.keys(payload.audio).some(key=>!['voice','format'].includes(key)) || payload.audio.format!=='wav' || typeof payload.audio.voice!=='string' || !/^[A-Za-z0-9][A-Za-z0-9_.:-]{0,79}$/.test(String(payload.audio.voice))))throw new Error('音频输出仅支持已授权 WAV 的非流式请求及明确 voice，实际扫描通过后才释放。')
  if(payload.audio!==undefined && payload.audio!==null && !audioRequested)throw new Error('audio 输出配置需要 modalities 明确包含 audio。')
}
