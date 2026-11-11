export interface RuntimeDetectionPolicy {
  id: number; rule: string; revision: number; enabled: boolean; windowMinutes: number
  threshold: number; minRequests: number; errorPercent: number; startHour: number; endHour: number
  timezoneOffsetMinutes: number; categories: string[]; createdAt?: string
}

export const runtimeDetectionLabels: Record<string, string> = {
  sensitive_response: '敏感数据响应', credential_response: '疑似凭据响应', missing_auth: '需认证接口匿名成功',
  subject_bulk_access: '主体访问频次', error_spike: '接口错误比例', off_hours: '非工作时段访问',
  sql_injection_signal: '疑似 SQL 注入', xss_signal: '疑似 XSS', path_traversal_signal: '疑似路径穿越',
}

export const runtimeDetectionDescriptions: Record<string, string> = {
  sensitive_response: '响应中的正格式统计达到阈值；原生 JSON 检查识别邮箱、手机号、身份证校验码和银行卡校验码，其他类别依赖可信来源的明确格式统计。字段名提示不会作为已确认格式命中。',
  credential_response: '响应中出现非空凭据命名字段，属于待核实信号，不能证明凭据有效或已发生泄露。',
  missing_auth: '仅当来源明确报告匿名、接口要求认证且返回 2xx 时触发。未知认证状态不会触发。',
  subject_bulk_access: '同一来源、同一去标识主体、同一接口在窗口内的请求次数。请求次数不代表取数行数或人数。',
  error_spike: '同一来源、同一接口窗口内 4xx 与 5xx 合计占比，同时满足最少请求数和错误次数。',
  off_hours: '按策略固定时区，统计配置时段内的请求次数；可跨午夜，不自动判断访问意图。',
  sql_injection_signal: '来源端有界请求模式识别，只上传枚举和计数，不上传原始请求或 SQL。',
  xss_signal: '来源端观察到疑似脚本模式，不证明应用可被执行或已存在 XSS 漏洞。',
  path_traversal_signal: '来源端观察到疑似目录穿越模式，不证明文件可读或已发生入侵。',
}

export interface RuntimePolicyPage {
  items: RuntimeDetectionPolicy[]; retentionHours: number; sampleLimitPerSource: number; windowLimitMinutes: number
}
