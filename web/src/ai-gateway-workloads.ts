export interface GatewayWorkloadBinding {id:number;revision:number;name:string;applicationId:string;issuer:string;audience:string;subject:string;jwks:{keys:Record<string,unknown>[]};maxTtlSeconds:number;enabled:boolean;createdAt:string;updatedAt:string}
export interface GatewayWorkloadPage {items:GatewayWorkloadBinding[];hasMore:boolean;nextCursor:string}
export interface GatewayWorkloadDraft {id:number;revision:number;name:string;applicationId:string;issuer:string;audience:string;subject:string;jwksJSON:string;maxTtlSeconds:number;enabled:boolean}
export function workloadDraft(binding?:GatewayWorkloadBinding):GatewayWorkloadDraft {
  return {id:binding?.id || 0,revision:binding?.revision || 0,name:binding?.name || '',applicationId:binding?.applicationId || '',issuer:binding?.issuer || '',audience:binding?.audience || '',subject:binding?.subject || '',jwksJSON:JSON.stringify(binding?.jwks || {keys:[]},null,2),maxTtlSeconds:binding?.maxTtlSeconds || 300,enabled:binding?.enabled ?? true}
}
export function workloadPublicKeys(input:string):{keys:Record<string,unknown>[]} {
  if(new TextEncoder().encode(input).length>16384)throw new Error('公钥 JWKS 超过 16 KiB，请缩小到本绑定实际需要的公钥。')
  let jwks:unknown
  try{jwks=JSON.parse(input)}catch{throw new Error('请导入合法的公钥 JWKS JSON。')}
  if(!jwks || typeof jwks!=='object' || Array.isArray(jwks) || Object.keys(jwks).some(key=>key!=='keys') || !Array.isArray((jwks as {keys?:unknown}).keys))throw new Error('JWKS 仅接受包含 keys 数组的对象。')
  const keys=(jwks as {keys:unknown[]}).keys
  if(!keys.length || keys.length>8)throw new Error('每个绑定需要 1—8 个明确公钥。')
  const seen=new Set<string>(),result:Record<string,unknown>[]=[]
  for(const value of keys) {
    if(!value || typeof value!=='object' || Array.isArray(value))throw new Error('每个 JWK 应为公钥对象。')
    const key=value as Record<string,unknown>
    if(Object.keys(key).some(name=>!['kty','kid','use','alg','key_ops','n','e','crv','x','y'].includes(name)))throw new Error('只接受公钥参数；请移除私钥、对称密钥、证书或远程地址字段。')
    if(typeof key.kid!=='string' || !/^[A-Za-z0-9][A-Za-z0-9_.:-]{0,79}$/.test(key.kid) || seen.has(key.kid) || key.use!=='sig' || (key.key_ops!==undefined && (!Array.isArray(key.key_ops) || key.key_ops.length!==1 || key.key_ops[0]!=='verify')))throw new Error('公钥 kid 须唯一且明确，use 必须为 sig，key_ops 仅可省略或为 ["verify"]。')
    seen.add(key.kid)
    const encoded=(name:string)=>typeof key[name]==='string' && /^[A-Za-z0-9_-]+$/.test(key[name] as string)
    if(key.alg==='RS256') {
      if(key.kty!=='RSA' || !encoded('n') || key.e!=='AQAB' || key.crv!==undefined || key.x!==undefined || key.y!==undefined)throw new Error('RS256 需要 RSA 公钥 n 与 e=AQAB；服务端另核对 2048—4096 位模数。')
    } else if(key.alg==='ES256') {
      if(key.kty!=='EC' || key.crv!=='P-256' || !encoded('x') || !encoded('y') || key.n!==undefined || key.e!==undefined)throw new Error('ES256 仅支持 P-256 公钥的 x/y 坐标。')
    } else if(key.alg==='EdDSA') {
      if(key.kty!=='OKP' || key.crv!=='Ed25519' || !encoded('x') || key.y!==undefined || key.n!==undefined || key.e!==undefined)throw new Error('EdDSA 仅支持 Ed25519 公钥 x。')
    } else throw new Error('公钥算法仅支持 RS256、ES256 或 EdDSA（Ed25519）。')
    result.push({...key})
  }
  return {keys:result}
}
export function workloadPayload(draft:GatewayWorkloadDraft) {
  if(!draft.name.trim() || !draft.applicationId || !draft.issuer.trim() || !draft.audience.trim() || !draft.subject.trim())throw new Error('请完整填写绑定名称、已保存应用和精确的 issuer、audience、subject。')
  const bytes=(value:string)=>new TextEncoder().encode(value).length
  if(bytes(draft.name.trim())>128 || bytes(draft.issuer.trim())>512 || bytes(draft.audience.trim())>512 || bytes(draft.subject.trim())>255 || [draft.name,draft.issuer,draft.audience,draft.subject].some(value=>/\p{Cc}/u.test(value)))throw new Error('名称最多 128 字节，issuer/audience 最多 512 字节，subject 最多 255 字节，不能含控制字符。')
  let issuer:URL
  try{issuer=new URL(draft.issuer.trim())}catch{throw new Error('issuer 必须为可信签发者的 HTTPS 地址。')}
  if(issuer.protocol!=='https:' || issuer.username || issuer.password || issuer.search || issuer.hash)throw new Error('issuer 应为不含凭证、查询或片段的 HTTPS 地址。')
  if(!Number.isSafeInteger(draft.maxTtlSeconds) || draft.maxTtlSeconds<1 || draft.maxTtlSeconds>300)throw new Error('短期凭据期限须为 1—300 秒整数。')
  if(draft.id && (!Number.isSafeInteger(draft.revision) || draft.revision<1))throw new Error('缺少已保存绑定的修订，请重新读取。')
  return {name:draft.name.trim(),applicationId:draft.applicationId,issuer:draft.issuer.trim(),audience:draft.audience.trim(),subject:draft.subject.trim(),jwks:workloadPublicKeys(draft.jwksJSON),maxTtlSeconds:draft.maxTtlSeconds,...(draft.id?{expectedRevision:draft.revision,enabled:draft.enabled}: {})}
}
export function workloadExchangeExample(slug:string,id:number,origin:string):string {
  return `POST ${origin}/api/v1/workspaces/${encodeURIComponent(slug)}/ai-gateway/workload-token\nAuthorization: Bearer <工作负载以受信任私钥签发的短期 JWT>\nContent-Type: application/json\n\n${JSON.stringify({bindingId:id},null,2)}`
}
