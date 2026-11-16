import { aiMediaInspectionVersion as mediaV2, aiLegacyMediaInspectionVersion as mediaV1, type AIRecord } from './ai-security-editor.ts'
import type { RuntimeEvent } from './ai-security-runtime'

export { mediaV2 }
const digest = /^[a-f0-9]{64}$/
export function mediaVersionState(value: unknown): string {
  return value===mediaV2?'v2 最低有效片段置信度':value===mediaV1?'历史 v1，保留原语义':typeof value==='string'&&value?'未知版本，需核对':'未记录媒体版本'
}
export function mediaDeclaration(model: AIRecord) {
  const value=model.media
  if(!value || typeof value!=='object' || Array.isArray(value))return undefined
  const media=value as AIRecord
  return { version:typeof media.inspectionVersion==='string'?media.inspectionVersion:'', mimeTypes:Array.isArray(media.allowedMimeTypes)?media.allowedMimeTypes.filter((x):x is string=>typeof x==='string'):[], maxAssetBytes:Number.isSafeInteger(media.maxAssetBytes)&&Number(media.maxAssetBytes)>0?Number(media.maxAssetBytes):undefined }
}
export function runtimeMediaEvidence(event: RuntimeEvent) {
  const raw=event.record?.decision
  if(!raw || typeof raw!=='object' || Array.isArray(raw))return undefined
  const decision=raw as AIRecord
  const version=typeof decision.mediaInspectionVersion==='string'?decision.mediaInspectionVersion:''
  const values=decision.mediaDigests
  if(!version && (!Array.isArray(values)||!values.length))return undefined
  const bound=Array.isArray(values)&&values.length>0&&values.length<=32&&values.every(x=>typeof x==='string'&&digest.test(x))&&digest.test(event.catalogDigest)&&digest.test(event.requestDigest)&&decision.catalogDigest===event.catalogDigest&&decision.requestDigest===event.requestDigest
  return { version, bound, digests:bound?values as string[]:[], principalId:typeof decision.principalId==='string'?decision.principalId:'' }
}
