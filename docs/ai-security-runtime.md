# AI security engine and customer runtime

`backend/pkg/aisecurity` provides catalog-backed authorization and an explicit
customer-side execution service. Applications must call this service to receive
its enforcement. It does not intercept arbitrary network traffic or replace a
SASE product. A control-plane evaluation is a policy preview; only customer
runtime execution produces an execution receipt.

## Go contract

The authoritative JSON structs are in `backend/pkg/aisecurity/types.go` and
`runtime_types.go`. JSON uses lower camel case; dates use RFC3339 timestamps.

```go
func DefaultConfig() Config
func ValidateConfig(Config) error
func ConfigDigest(Config) string
func RequestDigest(Request) string
func Evaluate(Config, Request, time.Time) Decision
func NewRuntime(RuntimeOptions) (http.Handler, error)
func OpenReplayJournal(string) (*ReplayJournal, error)
func AppendAudit(string) (func(AuditRecord) error, func() error, error)
func ToolDefinitionDigest(json.RawMessage) (string, error)
```

`Evaluate` is pure: it neither contacts an upstream nor consumes a nonce.
`ConfigDigest` hashes the JSON configuration without `approvals`, so adding a
bound approval does not invalidate its own catalog binding. `RequestDigest`
excludes `approvalId` and normalizes timestamps to UTC; it binds the request ID,
identity, expiry, input, target, action, parameters and other request fields.
These digests are references, not signatures or encryption. Serve the catalog
from an authenticated control plane or protected local configuration.

The default configuration is a valid empty, deny-by-default catalog. Its
`version` is `ai-security-v1`, policy version is `ai-policy-v1`, maximum request
age is 300 seconds, input/output limits are 65,536 bytes and all allowlists are
empty. Empty category arrays allow no detected or trusted classified category;
they never inherit a broader model policy.

The current runtime contract is `scarlet-ai-runtime-v2`; its inspector version
is `datasafety-four-format-v1`. Optional policy fields
`requiredRuntimeProtocolVersion` and `requiredInspectionVersion` must exactly
match those constants when present. Unknown profiles are rejected; there is no
automatic downgrade. New optional configuration fields use `omitempty`, so
unchanged legacy catalogs retain their existing digest and approval binding.
Enabling a profile, lease or definition pin changes the catalog digest and
requires a fresh bound approval. Older runtimes reject these unknown fields
when reading private configuration; they must not silently drop them.

| Config field | Trusted facts and enforcement |
| --- | --- |
| `applications` | Enabled application, permitted principal/document/model/tool IDs. |
| `principals` | Enabled principal and permitted application IDs; both membership directions must agree. |
| `documents` | ID, source/version, ACL version, knowledge base, permitted principals, business categories, optional `integrityDigest`. No document body is accepted here. |
| `models` | Enabled model, permitted applications, static `endpointRef`, declared provider/region, `upstreamModel`, per-model input and output category allowlists. |
| `tools` | Enabled tool, permitted applications/principals, literal actions/targets, parameter value allowlists, required parameters, high-impact flag, optional protocol adapter. |
| `policy` | Global category allowlists, permitted declared model regions, bounded input/output rules and limits. |
| `approvals` | Independent control-plane approvals bound to request/catalog digests and application/principal/tool/action/target, with expiry and revocation. Never supplied by an execution caller. |

IDs match `[A-Za-z0-9][A-Za-z0-9_.:-]{0,79}`; request IDs must also be at
least 16 characters. Business categories match `[a-z][a-z0-9_.-]{0,63}` and may
include labels such as `confidential` or `source_code`. Optional integrity hashes
are 64 lowercase hexadecimal characters. Limits are checked by
`ValidateConfig`: request age 1–3,600 seconds, content budgets 1–1,048,576 bytes,
and at most 32 configured RE2 expressions per direction, each at most 256 bytes.
Duplicate entities, dangling references and invalid parameter/protocol bindings
are rejected.

Application/model/tool `version`, `sourceRef` and `integrityDigest` are inventory
declarations. This service does not download and verify their executable
artifacts. A document integrity pin has a separate, implemented content check
described below. Declared provider/region and a static HTTPS endpoint do not
independently prove the provider's internal routing, physical processing region
or retention behavior.

## Authenticated execution

The runtime exposes only `POST /v1/execute`. It requires an uncompressed
`application/json` request and a bearer credential bound to exactly one
application/principal pair. The private binding contains the SHA-256 hash of
the bearer token, not a user-provided identity claim. Credential hashes and
static endpoint bindings are loaded at startup; restart to change them. The
catalog and local document source are reloaded during requests.

`RuntimeRequest` accepts these fields:

| Field | Meaning |
| --- | --- |
| `requestId`, `issuedAt`, `expiresAt` | Unique nonce and bounded validity. Keep the same request when obtaining its approval. |
| `kind` | Exactly `rag`, `model` or `tool`. |
| `documentIds` | Requested catalog document IDs for RAG; optional authorized context references for a model. |
| `modelId`, `input` | Catalog model ID and the JSON text adapter payload encoded as a string. |
| `toolId`, `action`, `target`, `arguments` | Catalog tool, literal authorized action/resource and string-valued parameter map. |
| `approvalId` | Reference to a trusted, digest-bound approval for a high-impact tool action. |

Unknown fields are rejected. In particular the runtime wire format has no
`applicationId`, `principalId`, `output`, ACL, category, endpoint URL, provider
credential or approval body. The internal `Request` includes authenticated
identity and output for previews and post-execution inspection; that broader
type must not be exposed as an unauthenticated execution request.
Duplicate JSON object keys and invalid UTF-8 are rejected before decoding the
wire request or upstream model/MCP objects. Parsing is bounded to 1 MiB plus
64 KiB, depth 64 and 131,072 values; stricter per-content budgets still apply.

Unknown or disabled applications, principals, documents, models and tools are
denied. A high-impact action requires an unexpired, unrevoked approval with
matching request/catalog digests and identity/action/target. Changing parameters,
expiry, principal or catalog invalidates the approval. The control plane must
authenticate the independent approver; a caller-written `approvedBy` string is
not an approval process.

Principals and documents may have `authorizationExpiresAt` (RFC3339). A
declared expiry at or before the current time always denies the affected
operation. With `policy.requireAuthorizationLeases: true`, missing expiries also
deny it. This applies to RAG ACLs and all accessible model context documents,
including when a caller omits document references. The runtime rechecks leases
before forwarding, after a blocking definition lookup, and before releasing
output. Without required leases, legacy entries without an expiry remain
usable; coverage records an unbounded principal authorization. A trusted source
adapter/admin must set and refresh leases after checking the real authority.
These timestamps bound stale authorization; they do not themselves synchronize
an identity provider or prove immediate external revocation. No automatic lease
renewal or maximum lease duration is imposed.

## RAG: authorization before content release

RAG executes an explicit document-ID retrieval gate. The catalog holds ACLs;
the private local document source contains only `{id, version, content}` rows.
The runtime authorizes every requested document before loading that source,
matches the actual source version and rechecks the current catalog before
releasing content. Revocation or a catalog change suppresses the result.

When catalog `Document.integrityDigest` is set, SHA-256 of the actual UTF-8
`content` bytes must match it, even if the source version is unchanged. A
mismatch returns `document_integrity_mismatch` without returning content.
Coverage records `local_document_sha256_verified`. Without a pin, coverage is
`local_document_version_only_unpinned_content`; a version declaration alone
does not prove content integrity. Content is also checked against the output
budget and configured output policy before release.

This is not vector search, a ranking engine or an automatic synchronization of
external knowledge-base permissions. A customer adapter must preserve this
authorization boundary when connecting a real knowledge base. The bundled
local loader reads the private document snapshot as a whole; only documents
that pass the trusted catalog ACL may have their content released to the
caller. Storage-side per-document retrieval filtering requires the customer
adapter and is not provided by the local snapshot loader.

## Model input and output

The implemented adapter accepts one JSON object in `input`, with exactly one
text input shape: `prompt` string, `input` string, or `messages` array of
`{role, content}` strings. Roles are `system`, `user`, `assistant`; at most 128
messages are accepted. The other accepted top-level fields are `model`,
`stream`, `max_tokens` and `temperature`.

`stream` must be false or absent. `max_tokens` is 0–4,096; zero/absent becomes
1,024. `temperature`, when provided, is 0–2. Unknown routing, deployment, tools,
URLs, multimodal fields and message extensions are rejected. The service
injects the catalog `upstreamModel`, falling back to the catalog model ID. A
caller-provided `model` must match it. The static `endpointRef` selects the
trusted URL; the caller cannot choose another provider or deployment. Providers
with other request protocols require a dedicated adapter.

Input categories must pass both global policy and the selected model's input
allowlist. Output must pass both global and that model's output allowlist.
Trusted business categories of all enabled documents available to the current
application/principal conservatively taint the model context, even if the caller
omits `documentIds`. Those categories and detected input categories propagate
to the output; a plain-text summary cannot silently declassify a confidential
source. This conservative rule may deny a request that did not actually use an
accessible document; use a narrower trusted application/catalog scope when
needed.

Automatic content inspection reuses `pkg/datasafety` for its four supported
format categories: email, phone, national ID and financial account. It scans
bounded text and JSON-decoded strings and rejects unsupported/partial inspection.
Configured input/output RE2 patterns check raw text and decoded JSON string
tokens, including Unicode escapes. These are explicit format and rule checks,
not general semantic classification or universal prompt-injection detection.
The runtime buffers a bounded complete JSON response before checking and
releasing it; model SSE and multimodal inference are unsupported.

`Model.responseFormat` is optional: empty/`json` preserves the generic JSON
response contract, with coverage explicitly stating that no provider response
schema/model identity was verified. `openai_chat` enables a fixed text response
adapter: one `chat.completion` choice at index zero, an `assistant` message with
string content, `stop` or `length` finish reason, and a reported model exactly
matching `upstreamModel` (or the catalog ID). The accepted top-level metadata is
`id`, `object`, `created`, `model`, `choices`, `usage`, `system_fingerprint`,
`service_tier`; choices may also contain `logprobs`, and messages may contain an
empty/null `refusal`. Unknown fields, nonempty refusal, tool/function calls,
multimodal content and HTTP-200 error bodies are suppressed. Only
`{model, output, finishReason}` is returned, after inspecting that normalized
text/JSON and applying the existing context category taint. Provider metadata
is not passed through. This checks the response's reported identity; it does not
independently attest the provider's internal model or processing geography.

## Tool and MCP adapters

`Tool.protocol` is empty/`json` for the existing JSON adapter or `mcp_http` for
the restricted stateless MCP HTTP adapter. JSON tools receive
`{action, target, arguments}` at their static endpoint.

For `mcp_http`, `targetArgument` is required and must be a valid ID. It cannot
also appear in `allowedArguments` or `requiredArguments`; a request containing
that parameter is rejected. The runtime copies authorized string parameters
and injects the independently authorized `target` into this parameter. It sends
JSON-RPC 2.0 `tools/call`, using `action` as `params.name` and the original
`requestId` as the RPC ID.

This adapter supports the **2026-07-28** protocol revision. It sends the
`MCP-Protocol-Version`, `Mcp-Method: tools/call` and `Mcp-Name` headers, plus
the required protocol/client information and empty client capabilities in
`params._meta`. It advertises `application/json, text/event-stream`. There is
no legacy initialize/initialized handshake or session ID.
`Tool.protocolVersion`, when supplied, must be `2026-07-28`; empty preserves
that existing profile. It is invalid on a plain JSON tool. The official
`specification/latest` redirected to this revision when checked on 2026-11-14.
Future protocol support requires an explicit implementation/profile change.

JSON responses and bounded SSE aggregation are supported. SSE is fully buffered
within the output byte budget and 10-second upstream timeout, permits at most
128 data events, ignores supported progress/message notifications and requires
exactly one final response with the matching string RPC ID. Nothing streams
through to the caller. There is no automatic reconnect, resume or retry.

The final response must have `resultType: complete`, no JSON-RPC error and no
`isError: true`. Supported content is strictly `content[]` entries with
`type: text` and `text`, optional `structuredContent` JSON, and result `_meta`.
The complete returned JSON is inspected before release. Images, audio,
resource/resource-link content, unknown content extensions, MRTR/input-required
responses, mismatched IDs, multiple final responses and legacy session headers
are rejected. HTTP 200 alone cannot produce a successful execution receipt.
This is a fixed-tool text/JSON MCP subset: no dynamic tool enrollment, schema negotiation,
stdio, MCP OAuth/user delegation, dynamic header schemas or arbitrary MCP proxy.

Optional `Tool.definitionDigests` maps every allowed action to a reviewed,
64-character lowercase SHA-256 digest of that action's complete MCP Tool object.
When nonempty, it must cover every configured action and contain no extra keys.
The runtime uses the same static endpoint and upstream credential to perform
`tools/list` immediately before an authorized call. It accepts only one complete
bounded JSON/SSE list page, at most 256 tools, without a nonempty `nextCursor`.
The selected action must occur exactly once and match its pinned definition.
Changed descriptions, annotations, schemas or extension values fail closed;
there is no dynamic enrollment of newly discovered tools. Missing/ambiguous
definitions, metadata errors, pagination or drift prevent `tools/call` and
consume the reserved nonce. A denied request does not contact the provider;
definition mismatch can produce a read-only lookup but no tool operation.

Compute a pin from the **single Tool object**, not the entire list or raw file
bytes, with `ToolDefinitionDigest`, or the offline CLI mode:
`ai-security-agent -tool-definition /private/reviewed-tool.json`. The CLI
requires a private regular file and prints only digest/protocol JSON without
starting the service. The algorithm sorts object keys through Go
`encoding/json` and preserves JSON number spelling; whitespace/key order do not
change the hash, while `1` versus `1.0` may. It is not RFC 8785. Definitions are
bounded to 64 KiB, depth 32 and 8,192 values, reject duplicate keys, require an
object `inputSchema`, and reject `x-mcp-header` because this adapter does not
implement parameter-to-header routing. The entire Tool object is pinned,
including descriptions and annotations, but those fields never grant authority.
The configured literal action/target/parameter rules and independent approval
remain mandatory. Pinning detects advertised definition drift, not a malicious
server that changes its implementation while advertising the same definition.
The list-to-call check is a pre-call snapshot: it cannot atomically lock the
remote definition between lookup and invocation. The same trusted endpoint and
its behavior remain part of the customer trust boundary.
Without a pin, coverage explicitly says the tool definition is unpinned.

The protocol references are the official
[2026-07-28 tools specification](https://modelcontextprotocol.io/specification/2026-07-28/server/tools),
[Streamable HTTP transport](https://modelcontextprotocol.io/specification/2026-07-28/basic/transports/streamable-http)
and [base protocol](https://modelcontextprotocol.io/specification/2026-07-28/basic/index).

## Local service deployment

Build with `go -C backend build ./cmd/ai-security-agent`. Required flags are
`-config`, `-bindings`, `-audit` and `-journal`; `-documents` is required when
using RAG. Production also requires `-tls-cert` and `-tls-key`. `-listen`
defaults to `127.0.0.1:8900`; `-concurrency` defaults to 8 and is bounded 1–32.

Configuration/document files must be private regular files with no group/other
permission bits. Maximum file sizes are 4 MiB for the catalog, 1 MiB for bindings
and 8 MiB for the document source. Unknown fields and trailing JSON are rejected.
Bindings contain `credentials: [{tokenHash, applicationId, principalId}]` and
`targets: {endpointRef: {url, authorizationEnv?}}`. `authorizationEnv` refers to
an environment variable holding an upstream bearer token; raw credentials and
upstream authorization are never part of the catalog or audit record. Protect
the configuration directory and process environment against untrusted writers.

Upstream URLs are static HTTPS URLs without embedded credentials, query or
fragment. TLS verification is enabled; TLS 1.2 is the minimum. Redirects,
environment proxies and compressed responses are disabled/rejected. Only
`-local` permits HTTP for synthetic tests, and both listening and HTTP upstream
addresses must be literal loopback IPs. It cannot bind local HTTP to all
interfaces. Incoming request, header, output, concurrent execution and upstream
time limits are bounded. This runtime does not pull control-plane configuration
or transparently enroll customer sources; a trusted deployment process must
supply the approved catalog and local source bindings.

## Replay, audit and failure semantics

Every authorized request reserves its application/principal/request-ID nonce
in a durable journal before any upstream action. The same nonce cannot execute
again after success, failure or restart, even with changed parameters. A
`prepared` audit record is synced before forwarding. The runtime sends an
`Idempotency-Key` derived from the request digest; upstream services should
implement their own durable deduplication as well.

Use one active runtime with the same durable journal for a credential scope.
Independent journals do not provide distributed deduplication. An exclusive
`.lock` prevents concurrent journal use; after a crash, an operator must verify
the old process has stopped before removing its stale lock. The journal fails
closed on invalid records, 32 MiB input size or 100,000 consumed nonce entries.
Do not delete/rotate it to retry a possibly executed action; reconcile upstream
effects first and plan maintenance outside active credential scopes.

`AuditRecord` contains `eventId`, `requestId`, `attemptId`, `stage`, `observedAt`, `decision`
and `execution`, plus `runtimeProtocolVersion`, `inspectionVersion`, and
`mcpProtocolVersion` when a known MCP tool is involved. Older receipts without
versions are unknown coverage, not evidence that the new profile was enforced.
Stages are `decision`, `prepared`, `completed`.
Each accepted HTTP attempt receives a runtime-generated random `attemptId`;
its stages share that reference and retries receive a different one. Callers
cannot provide it. `Execution` contains `attempted`, `outcome`, and optional `httpStatus`. Outcomes are
`not_attempted`, `succeeded`, `failed`, `unknown` or `blocked_output`. Records
include catalog version/digest and request digest without body, parameter
values, endpoint URLs or credentials. Local JSONL audit writes are synced.
Audit/configuration unavailability before execution denies the operation.

`succeeded` means the local source read or upstream protocol response completed
successfully and passed the implemented output guard. It is not independent
verification of a business side effect or proof that the caller received the
response. A timeout, incomplete response or post-execution audit failure can
leave an upstream side effect **unknown**. Output is suppressed; the nonce stays
consumed. The service neither claims rollback nor automatically retries the
action. Catalog/ACL/approval changes detected after execution also suppress
output but cannot undo a side effect already performed upstream.

The platform's separate runtime-event source/forwarder can ingest these
content-free receipts. This binary writes local durable audit; it has no
embedded platform token or automatic platform audit upload. Preserve local
records and reconcile pending/unknown receipts when the platform is offline.

## Verification

Run `go -C backend test -race ./pkg/aisecurity ./cmd/ai-security-agent` and
`go -C backend vet ./pkg/aisecurity ./cmd/ai-security-agent`. Tests use actual
local HTTP servers and durable temporary journals. They cover denial without
upstream calls, ACL revocation/cross-principal isolation, unchanged-version
document content replacement, target/model selection, output category taint,
escaped JSON rules, approval parameter binding, restart replay, audit failures,
redirects/budgets, lease expiry during execution, advertised definition drift,
revocation during definition lookup, normalized actual chat responses, and MCP
JSON/SSE/error/unsupported-result boundaries.
These are synthetic local acceptance tests; customer identity integration,
provider routing/residency evidence, real knowledge-base synchronization and
customer production acceptance remain separate deployment work.
