export interface RuntimeDetectionPolicy {
  id: number; rule: string; revision: number; enabled: boolean; windowMinutes: number
  threshold: number; minRequests: number; errorPercent: number; startHour: number; endHour: number
  timezoneOffsetMinutes: number; categories: string[]; createdAt?: string
  scopeKey?:string;sourceId?:number;method?:string;route?:string;subjectRef?:string;clientIp?:string;direction?:string;allowlistConfigured?:boolean;allowedCategories?:string[];allowedCountries?:string[];allowedClientCountries?:string[];allowedRecipients?:string[];allowedDomains?:string[];maxVolume?:number;volumeUnit?:string
}

export const runtimeDetectionLabels: Record<string, string> = {
  over_collection:'超出允许数据类型',credential_cleartext_transport:'认证材料明文传输',subject_multi_ip:'同一账号多IP',subject_multi_region:'同一账号多来源地域',client_region_mismatch:'来源地域超出允许范围',recipient_scope:'共享接收方超范围',destination_scope:'目的地超范围',volume_limit:'传输量超阈值',subject_repeated_access:'主体少量多次访问',
  sensitive_response: '敏感数据响应', credential_response: '疑似凭据响应', missing_auth: '需认证接口匿名成功',
  subject_bulk_access: '主体访问频次', error_spike: '接口错误比例', off_hours: '非工作时段访问',
  sql_injection_signal: '疑似 SQL 注入', xss_signal: '疑似 XSS', path_traversal_signal: '疑似路径穿越',
}

export const runtimeDetectionDescriptions: Record<string, string> = {
  over_collection:'对来源明确检查过的数据方向与类型统计，对照用户明确登记的允许列表；未检查完整时保持未知。',credential_cleartext_transport:'来源明确观察到认证材料字段并报告传输为HTTP，作为该链路的技术风险；不证明凭据有效。',subject_multi_ip:'同一来源内去标识主体的不同客户端IP观察数，不推断真实自然人或恶意。',subject_multi_region:'同一来源主体的来源地域观察数；地域必须由来源明示，不由平台从IP猜测。',client_region_mismatch:'将来源明示的客户端国家或地域与本策略允许列表比较，缺失地域保持未知。',recipient_scope:'对照用户明确登记的共享接收方列表；未提供接收方事实时保持未知。',destination_scope:'对照来源明示的目的地国家与域名，未采集目的地不得据此确认跨境。',volume_limit:'采用来源明示的记录量或字节量及单位；请求次数不能冒充数据行数。',subject_repeated_access:'在配置窗口内统计同来源去标识主体的反复访问次数，不据此判断人员身份或访问目的。',
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
