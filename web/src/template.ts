import type { ActivityKind } from './activity'

export type TemplateKind = Exclude<ActivityKind, 'internal'>

export const templateKinds: TemplateKind[] = ['sharing', 'cross_border', 'ai']

export interface TemplateItem {
  key: string
  title: string
  requirementCode: string
  applicability: string
  evidenceRequirements: string
  acceptanceCriteria: string
}

export interface CheckTemplate {
  id: number
  key: string
  title: string
  kind: TemplateKind
  version: number
  builtIn: boolean
  items: TemplateItem[]
  createdAt: string
}

export interface TemplateBinding {
  revisionId: number
  key: string
  title: string
  kind: TemplateKind
  version: number
  builtIn: boolean
  item: TemplateItem
}
