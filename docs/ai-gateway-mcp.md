# 受控 MCP 网关接口

`POST /mcp` 使用 MCP `2026-07-28` 无状态 HTTP 配置，提供
`server/discover`、`tools/list` 和 `tools/call`。请求与响应是 JSON-RPC 2.0，
支持字符串或整数 RPC ID，并保持响应 ID 一致。本配置不接受旧版 initialize/session
或独立 GET SSE，不自动执行 Agent 循环、MRTR、订阅或多模态工具结果。

协议依据：[当前版本与兼容规则](https://github.com/modelcontextprotocol/modelcontextprotocol/blob/main/docs/specification/2026-07-28/basic/versioning.mdx)、
[HTTP 头与正文一致性](https://github.com/modelcontextprotocol/modelcontextprotocol/blob/main/docs/specification/2026-07-28/basic/transports/streamable-http.mdx)。
旧版客户端须显式升级或使用单独验收的协议适配，不能自动降低执行安全要求。

## 认证与发现

所有方法使用同一工作区应用 Bearer 凭证。客户端必须提供 `Content-Type: application/json`、
`Accept: application/json, text/event-stream`、`MCP-Protocol-Version: 2026-07-28`
和匹配正文 method 的 `Mcp-Method`。`tools/call` 还需要匹配 name 的 `Mcp-Name`。
提供的 `Mcp-Param-*` 镜像值必须与对应字符串参数一致，包括 Base64 哨兵编码。
此入口面向服务端客户端；默认拒绝携带浏览器 Origin 的请求，不提供浏览器跨域授权。

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "server/discover",
  "params": {
    "_meta": {
      "io.modelcontextprotocol/protocolVersion": "2026-07-28",
      "io.modelcontextprotocol/clientInfo": {"name": "enterprise-agent", "version": "1"},
      "io.modelcontextprotocol/clientCapabilities": {}
    }
  }
}
```

工具列表从受信的本地安全目录构建，按应用、主体、工具开关、双向成员授权及授权租约过滤，并与控制面实时工具范围取交集。
它不会从远端临时下载并向模型传播任意工具说明。目录构建的参数 schema 只暴露字符串参数、
显式目标和允许值；调用时仍执行完整 Runtime 策略与控制面授权，发现结果不是执行许可。
控制面撤销范围后，工具名称、目标及参数枚举不继续出现在发现结果中；只读发现查询不预留预算或执行工具，控制面故障时也不回退为旧的本地允许列表。

## RPC ID 与执行 nonce

JSON-RPC ID 只用于协议关联，不作为批准或防重放依据。`tools/call` 的独立执行请求 ID
通过 `_meta["org.scarletplume/control"].requestId` 或 `Idempotency-Key` 提供；两者同时存在
必须相等。未提供时服务器生成随机值，响应头 `X-Scarlet-Request-ID` 返回该值。
改变 RPC ID 不能绕过相同执行 nonce 的防重放。

```json
{
  "jsonrpc": "2.0",
  "id": 2,
  "method": "tools/call",
  "params": {
    "name": "registered-tool-name",
    "arguments": {"targetKey": "approved-target", "parameter": "approved-value"},
    "_meta": {
      "io.modelcontextprotocol/protocolVersion": "2026-07-28",
      "io.modelcontextprotocol/clientCapabilities": {},
      "org.scarletplume/control": {
        "requestId": "stable-execution-nonce-1234",
        "issuedAt": "2026-11-15T00:00:00Z",
        "expiresAt": "2026-11-15T00:01:00Z",
        "approvalId": "independently-issued-approval"
      }
    }
  }
}
```

`targetKey` 必须替换为工具目录的 `targetArgument`，所有参数必须为字符串。
示例时间不可直接用于实际请求。高风险动作必须先按实际 Request 字段获得独立批准，
保持 requestId、issuedAt、expiresAt、参数、身份和安全目录摘要不变；批准只引用可信控制面的
记录。客户端不能用 `_meta` 的姓名、角色、approvedBy 或身份声明替代批准。

工具调用映射到注册的 toolId/action；目标由显式参数提取，不从提示词推断。
执行复用现有精确参数白名单、完整工具定义 pin、持久 nonce、调用前与输出前撤权复核、
输出检查和审计。上游只接收到可信 Runtime 构建的请求。响应将内部执行 nonce 与外部 RPC ID
分开处理，并丢弃上游 `_meta`，不把任意元数据继续传播给客户端。

保留的 `POST /v1/mcp/execute` 是早期受限自定义适配，不提供现代发现，也不应被配置为
通用 MCP 发现入口。`POST /v1/execute` 保留完整的原始 Runtime 执行契约。
