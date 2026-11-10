export const activityKindLabels = {
  internal: '内部处理',
  sharing: '对外共享或委托',
  cross_border: '数据出境',
  ai: 'AI 使用',
}

export type ActivityKind = keyof typeof activityKindLabels

export interface ActivityLink {
  id: number
  name: string
  kind: ActivityKind
}

export interface Activity extends ActivityLink {
  assetId: number
  assetName: string
  assetSystem: string
  purpose: string
  recipient: string
  region: string
  retentionPolicy: string
  transferPath: string
  safeguards: string
  createdAt: string
}

export interface ActivityPage {
  items: Activity[]
  nextCursor?: number | null
}

export const activityCheckHints: Partial<Record<ActivityKind, { code: string; scope: string; retest: string }>> = {
  sharing: {
    code: 'T01',
    scope: '核对批准记录、合同约定和实际权限，确认接收方及授权范围。',
    retest: '复测可核对已撤销授权是否消失，或用获准的测试身份验证越范围访问被拒绝。',
  },
  cross_border: {
    code: 'T03',
    scope: '核对数据流向、接收方和处理地域，记录适用路径的判断依据及审批或评估引用。',
    retest: '复测可对照整改后的实际目的地、访问或传输日志与已批准的数据流向。',
  },
  ai: {
    code: 'X01',
    scope: '核对模型服务处理条款、输入边界、处理地域及留存和训练设置。记录脱敏后的网关配置或日志引用。',
    retest: '复测可使用合成样本验证越过输入边界的请求被拒绝，记录规则版本与测试结果。',
  },
}
