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

JSON 字段名称区分大小写。请求必须是单个对象，包含 `jsonrpc: "2.0"`、非 null 的
`id`、支持的 `method` 和对象类型的 `params`；不接受批量请求、重复键、尾随 JSON
或字段大小写别名，例如 `ID`、`Method`、`Arguments`。RPC ID 为最长 256 字节、无
NUL/CR/LF 的 UTF-8 字符串或 64 位整数。正文 JSON 的硬上限为 16 MiB，实际工具输入
还受采用策略的独立限额约束。

| 方法 | `params` 允许字段 | 必需内容 |
| --- | --- | --- |
| `server/discover` | `_meta` | 协议版本及客户端能力 |
| `tools/list` | `_meta`、`cursor` | 协议版本及客户端能力；不接受非空 cursor |
| `tools/call` | `_meta`、`name`、`arguments` | 已登记工具名称，以及包含准确目标的字符串参数对象 |

所有方法的 `_meta` 都必须包含
`io.modelcontextprotocol/protocolVersion: "2026-07-28"` 和非 null 对象
`io.modelcontextprotocol/clientCapabilities`。`io.modelcontextprotocol/clientInfo` 可选；
提供时须有有效的名称和字符串版本。
`arguments` 的值必须是 JSON 字符串，不能用 null、数字、布尔值或对象替代；空字符串仍按
目录允许值判断，不会自动裁剪或改写。未列入对应方法的 `params` 字段会被拒绝。

HTTP 头名称按 HTTP 规则不区分大小写，协议字段与正文值仍须匹配：

- `MCP-Protocol-Version`、`Mcp-Method` 以及调用时的 `Mcp-Name` 必须各有且仅有一个值。
  `Accept` 必须同时接受 JSON 和 event-stream，`q=0` 或无效质量值不算接受。
- 每个已提供的 `Mcp-Param-*` 必须只有一个值，并且按不区分大小写的参数名只能匹配正文
  中唯一一个参数；重复头或参数名匹配歧义会被拒绝。头值使用原字符串比较；非 ASCII、
  前后空格等值可用 `=?base64?<UTF-8 标准 Base64>?=` 镜像编码。
- 任何 `Origin`、`Mcp-Session-Id`、`Last-Event-ID` 头都会被拒绝，包括空值。
  当前入口不接收压缩正文，也不建立会话或恢复流。

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
目录响应写出前，会再次核对原安全目录摘要、当前凭证身份、实时工具授权及原任务的完整祖先
租约。检查失败时不释放刚构建的目录，不会用新取得的任务租约为旧目录背书。发现响应的
`ttlMs` 为 0、`cacheScope` 为 `private`，客户端不得把发现结果作为可长期复用的执行许可。

## RPC ID 与执行 nonce

JSON-RPC ID 只用于协议关联，不作为批准或防重放依据。`tools/call` 的独立执行请求 ID
通过 `_meta["org.scarletplume/control"].requestId` 或 `Idempotency-Key` 提供；两者同时存在
必须相等。未提供时服务器生成随机值，响应头 `X-Scarlet-Request-ID` 返回该值。
改变 RPC ID 不能绕过相同执行 nonce 的防重放。
`Idempotency-Key` 若提供，必须恰有一个非空值；重复值即使相同也会被拒绝。
nonce 为 16—80 字符的有效运行时 ID。重试有副作用的调用时，应保留原 nonce 并先核对实际
执行记录，不能因响应丢失而省略 nonce 或请求服务器重新生成。

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
控制元数据对象只允许准确拼写的 `requestId`、`issuedAt`、`expiresAt`、`approvalId`；
`RequestID` 等大小写别名或其他字段会被拒绝。绑定任务通过 `X-Scarlet-Task-Id` 提供，
要求任务的应用必须显式绑定有效任务；任务标识不能代替原祖先租约的 epoch、范围与预算检查。
这些显式字段必须是 JSON 字符串，不能是 null；显式 `requestId` 不能为空，显式时间必须
为非零 RFC3339 时间。省略字段或空控制对象可采用服务器默认值，显式无效值不会被悄悄换成
新 nonce 或新有效期。`approvalId` 的空字符串仍表示没有独立批准，不会授予执行权限。

工具调用映射到注册的 toolId/action；目标由显式参数提取，不从提示词推断。
执行复用现有精确参数白名单、完整工具定义 pin、持久 nonce、调用前与输出前撤权复核、
输出检查和审计。上游只接收到可信 Runtime 构建的请求。响应将内部执行 nonce 与外部 RPC ID
分开处理，并丢弃上游 `_meta`，不把任意元数据继续传播给客户端。

## 工具结果与最终释放

上游响应只接受单个匹配执行 nonce 的最终 JSON-RPC 结果。响应可以是 JSON，或先完整缓冲
有界 SSE 后解析；允许的进度、日志通知被丢弃，不向客户端提前释放正文，也不自动重连。
SSE 识别 LF、CRLF 和裸 CR 行尾；数据事件须以空行结束，只有一行末尾换行或直接 EOF 都
不能结束该事件。EOF 仍有待处理
数据时整个响应被拒绝，即使前面已有完整结果，也不会忽略截断的尾事件后宣称成功。
这与 [SSE 事件解释规则](https://html.spec.whatwg.org/multipage/server-sent-events.html#event-stream-interpretation)
一致。未闭合工具响应按已尝试、结果未知保留审计及已消费的持久 nonce，不会自动重试工具。
重复最终响应、JSON-RPC error、ID 不匹配、字段大小写别名及未知字段均被拒绝。
上游必须提供唯一的 JSON 或 event-stream `Content-Type`，不能带会话头或非空内容编码；
若提供协议版本头，必须唯一且等于当前支持版本。

工具 `result` 只接受以下受限形态：

```json
{
  "resultType": "complete",
  "content": [{"type": "text", "text": "checked tool result"}],
  "isError": false,
  "structuredContent": {"status": "example"}
}
```

`resultType` 和非 null 数组 `content` 是必需字段；空数组可用。每个内容项只能有
`type: "text"` 与字符串 `text`，缺少 text 或 `text: null` 会被拒绝，空文本可以接受。
`isError` 可省略或为 `false`；`true`、null、其他类型，以及 `input_required` 等未知或
未完成结果均不会被包装成成功。可选 `structuredContent` 保留 JSON 类型并参加完整输出
数据检查；这不表示已实现任意远端 `outputSchema` 验证。可选 `_meta` 必须是非 null 对象，
但其内容不会向客户端透传，网关会替换为自己的服务信息和执行请求引用。

工具结果中的图片、音频、内嵌资源和资源链接不属于本配置的允许内容；资源方法
`resources/list`、`resources/templates/list`、`resources/read` 也不受支持。即使收到 URI，
网关也不会代为读取或下载资源。结构化结果中的数据仍须通过输出策略，不因外形合法而放行。

输出检查通过后，MCP 包装层还会在写出前核对**原执行时冻结的请求、决策及可信上下文**，
包括原凭证、目录修订、任务祖先租约及其撤销和预算约束，以及当前请求的取消和截止期限。
不会重新获取来源或媒体检查来为旧结果背书。最终复核失败只阻止结果释放：已经执行的工具
副作用、实际调用费用和原执行审计仍然保留，不能把它们重标成“未执行”或自动退款。
因此，客户端收到错误或丢失响应时须先核对原执行记录，不能通过更换 nonce 自动重试高影响动作。

保留的 `POST /v1/mcp/execute` 是早期受限自定义适配，不提供现代发现，也不应被配置为
通用 MCP 发现入口。`POST /v1/execute` 保留完整的原始 Runtime 执行契约。
