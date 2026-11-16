# 私网授权资料源连接器

网关可按一次请求的真实应用、主体和资料 ID，通过固定 HTTPS 资料源读取正文。源端授权与已发布安全目录取交集；不允许客户端选择 URL、提交可信 ACL 或替换正文摘要。此连接器实现 `scarlet-document-source-v1`，不代表已经接入客户的 SharePoint、Confluence 或其他知识库。

## 部署与身份边界

```sh
scarlet-ai-gateway \
  ... \
  -workspace actual-workspace \
  -document-source-url https://private-source.example/resolve \
  -document-source-token-file /config/document-source.token
```

资料源与 `-documents` 快照二选一。Compose 启用 HTTP 源时，显式设置 `SCARLET_GATEWAY_DOCUMENTS_FILE=` 关闭默认快照，再设置 `SCARLET_GATEWAY_DOCUMENT_SOURCE_URL`，并私有挂载 `/config/document-source.token`。生产环境要求 HTTPS、有效服务端证书及固定工作区；本地合成测试才允许 literal loopback HTTP。令牌文件须为仅部署进程可读的有界文件。

源服务必须将 Bearer 凭证绑定到一个工作区，并独立查询源系统对指定主体的当前授权。不能把请求中出现的 `principalId` 或目录配置本身当作源端已经允许的证明。网关从已认证身份构造查询，工作区来自固定部署；请求调用者不能覆盖这些事实。不同工作区应采用不同源凭证及明确的资料命名空间。

## 请求与响应契约

请求为认证的 `POST`，不转发应用客户端密钥：

```json
{
  "profile": "scarlet-document-source-v1",
  "workspaceSlug": "actual-workspace",
  "applicationId": "support-app",
  "principalId": "support-worker",
  "documentIds": ["approved-document"]
}
```

成功响应须精确回显工作区及主体，返回请求的全部资料且不得增加、遗漏或重复。以下正文和摘要仅为格式示意，部署时须按实际 UTF-8 正文计算 SHA-256：

```json
{
  "profile": "scarlet-document-source-v1",
  "workspaceSlug": "actual-workspace",
  "applicationId": "support-app",
  "principalId": "support-worker",
  "documents": [{
    "id": "approved-document",
    "version": "v1",
    "aclVersion": "acl-v1",
    "content": "Synthetic enterprise note",
    "contentDigest": "<64 lowercase hexadecimal SHA-256 characters>",
    "authorized": true,
    "authorizationExpiresAt": "<RFC3339 time after now and no later than five minutes from now>"
  }]
}
```

正文允许普通换行。每次最多 64 份资料，单份正文最多 1 MiB、合计最多 4 MiB；实际运行时策略、模型输入和检测预算仍分别约束。响应限 8 MiB，要求无压缩的 JSON，10 秒客户端期限；不跟随重定向、不使用环境 HTTP 代理。未知 profile、重复键、额外字段、身份/工作区错配、ACL/版本漂移、缺失授权、租约无效、摘要不符、源不可用或请求取消均拒绝。

## 实际执行

RAG 返回前核对目录权限与源端授权，扫描实际正文并再次读取来源确认。模型请求明确引用 `documentIds` 时，网关读取、核验和扫描实际正文，再作为清楚标明来源数据的消息注入固定上游；请求不能只登记文档 ID 来冒充已使用真实资料。来源文本中的指令没有授予工具权限的效力，动作仍受任务范围、目录精确约束和独立批准限制。

来源证据包含资料 ID、版本、ACL 版本和实际正文 SHA-256。真实读取过的来源绑定及敏感类别写入任务历史，后续步骤即使省略 `documentIds`，仍复核历史来源和用途、留存、训练限制；来源发生变化或撤权时抑制输出。正文、应用输入输出与源令牌不进入控制面任务审计。

有 `usePolicy` 的来源及其任务派生内容不进入响应缓存。其他受支持文本缓存的键包含实际来源绑定、任务、用途、范围和类别，命中仍重新授权并检查输出。固定目录摘要是发布检查点；源端 ACL/版本变化不会被网关静默批准。

## 用途与目的端约束

资料可配置：

```json
"usePolicy": {
  "allowedPurposes": ["customer-support"],
  "maxRetentionHours": 0,
  "allowTraining": false
}
```

用途来自已授权管理员创建的任务。空用途列表不放行，`0` 表示要求声明零留存；默认不允许训练使用。模型和外发工具可配置 `handling`：

```json
"handling": {
  "retentionHours": 0,
  "trainingUse": "prohibited",
  "evidenceRef": "approved-dpa-v1",
  "authorizationExpiresAt": "<RFC3339 approved-contract expiry>"
}
```

留存值范围为 0—8760 小时；训练声明仅接受 `prohibited` 或 `permitted`。目的端约定缺失、过期或超过资料限制时拒绝。`requireDataHandling` 可要求所有模型与工具都具备当前约定，`requirePinnedSourceContent` 可要求实际读取资料具备发布时的正文固定摘要。

这些约定是经审核、有期限的目的端声明和证据引用，不是对供应商内部训练或删除行为的独立技术证明。实际合同、来源权限映射、客户源适配和运行性能仍需在对应环境验收。
