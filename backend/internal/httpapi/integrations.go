package httpapi

type integration struct {
	ID         string `json:"id"`
	Label      string `json:"label"`
	Category   string `json:"category"`
	Capability string `json:"capability"`
	Status     string `json:"status"`
	Docs       string `json:"docs"`
}

var integrations = []integration{
	{"postgresql", "PostgreSQL", "资产发现", "定期读取指定 schema 的表目录。平台托管任务还采集字段和直接 ACL，并向关联检查登记证据。", "available", "https://www.postgresql.org/docs/18/catalog-pg-class.html"},
	{"openmetadata", "OpenMetadata", "数据目录", "按指定服务定期采集数据库及表目录，自动登记资产并保留源端引用。接口已通过模拟验证。", "available", "https://docs.open-metadata.org/v2.0.x/api-reference/data-assets/databases/list"},
	{"aliyun-dsc", "阿里云 DSC", "分类分级", "读取扫描范围及分类发现，提交人工复核。", "planned", "https://help.aliyun.com/zh/dsc/data-security-center/developer-reference/api-sddp-2019-01-03-overview"},
	{"tencent-dsgc", "腾讯云 DSGC", "风险评估", "读取风险项及扫描范围，关联资产和控制。", "planned", "https://cloud.tencent.com/document/api/1087/97236"},
	{"huawei-dsc", "华为云 DSC", "分类分级", "采集扫描任务状态、执行时间和资源范围。", "planned", "https://support.huaweicloud.com/api-dsc/ShowScanJobList.html"},
	{"feishu", "飞书云文档", "访问控制", "定期采集指定直接协作者的权限快照，自动登记 G03 检查及整改复测证据。真实租户待联调。", "available", "https://open.feishu.cn/document/server-docs/docs/permission/permission-member/list"},
	{"feishu-approval", "飞书审批", "外发与协同", "接入审批实例并与文档权限证据关联。", "planned", "https://open.feishu.cn/document/server-docs/approval-v4/instance/get"},
	{"aliyun-oss", "阿里云 OSS", "访问控制", "结合阻止公共访问、桶策略和 ACL 检查共享配置。", "planned", "https://help.aliyun.com/zh/oss/user-guide/block-public-access"},
	{"wazuh", "Wazuh", "事件证据", "接收客户侧过滤的告警元数据，进入待研判。", "planned", "https://documentation.wazuh.com/current/user-manual/manager/integration-with-external-apis.html"},
	{"feilian", "火山引擎飞连", "终端防泄漏", "接入支持外发的终端与通道防护日志，核对策略和处置结果。", "planned", "https://docs.volcengine.com/docs/feilian/log-audit-and-forwarding?lang=zh"},
	{"sls", "阿里云 SLS", "日志与留存", "读取日志库保留设置，并关联授权时间窗的检索验证。", "planned", "https://help.aliyun.com/zh/sls/developer-reference/api-sls-2020-12-30-getlogstore"},
	{"keycloak", "Keycloak", "企业身份", "通过 OIDC 对接企业身份，平台单独管理工作区成员权限。", "planned", "https://www.keycloak.org/securing-apps/oidc-layers"},
	{"openbao", "OpenBao", "密钥管理", "由客户侧管理连接器凭据及密钥，保留可审计的访问引用。", "planned", "https://openbao.org/docs/secrets/kv/kv-v2/"},
	{"higress", "Higress", "AI 使用", "接入受控模型 API 的脱敏和阻断证据。", "planned", "https://higress.cn/docs/latest/plugins/ai/api-consumer/ai-data-masking/"},
}

func availableProvider(provider string) bool {
	return provider == "postgresql" || provider == "openmetadata"
}
