import type { RuntimeEvidenceScope } from './runtime-evidence-scope'
import type { ControlActionInput, ControlResult } from './security-operations'

export interface ScenarioCheckBasis { title: string; clause: string; version: string; url: string }
export interface ScenarioCheckFinding {
  id: string; title: string; outcome: string; requirementCode: string; basis: ScenarioCheckBasis; ruleVersion: string
  expected: string; observed: string; evidenceRefs: string[]; coverage: string; missingConditions: string[]
  remediation: { advice: string; executable: boolean; kind?: string; reason: string; target?: ControlActionInput }
  checkId?: number; actionId?: number
}
export interface ScenarioCheckEvidence {
  id: string; source: string; record: string; observedAt?: string; digest: string; scope: string; snapshot: unknown
}
export interface ScenarioCheckReportRequirement {
  key: string; title: string; basis: ScenarioCheckBasis; applicability: string; requiredDeliverables: string[]
  findingIds: string[]; evidenceRefs: string[]; coverage: string; status: string; missingConditions: string[]
  externalSteps: string[]; retentionNote: string
}
export interface ScenarioCheckAction {
  findingId: string; actionId: number; status: string; revision: number; createdAt: string; result?: ControlResult
}
export interface ScenarioCheckRun {
  id: number; scenario: string; task: string; scope?: RuntimeEvidenceScope; assetId: number; assetName: string; activityId?: number
  assetRevision: number; activityRevision?: number; status: string; outcome: string; mode: string; engineVersion: string
  createdAt: string; createdBy: number; actor: string; counts: { compliant: number; nonCompliant: number; needsVerification: number }
  findings?: ScenarioCheckFinding[]; evidence?: ScenarioCheckEvidence[]; reportRequirements?: ScenarioCheckReportRequirement[]
  snapshotDigest: string; previousRunId?: number; retestState: string; warnings: string[]; actions?: ScenarioCheckAction[]
}
export const scenarioCheckOutcomeLabels: Record<string, string> = { compliant: '符合', non_compliant: '不符合', needs_verification: '待核实' }
export const scenarioCheckRetestLabels: Record<string, string> = { not_retested: '尚未关联复测', new_source_observation: '已采用新来源观察', same_source_observation: '相同来源事实重查，未取得新来源复测' }
export const scenarioCheckReportLabels: Record<string, string> = { available: '报告内容已形成', partial: '报告内容待补', missing: '报告证据缺失' }

export const scenarioCheckApplicabilityLabels: Record<string, string> = { applicable: '适用', conditional: '按条件适用', unknown: '待核实适用性' }
