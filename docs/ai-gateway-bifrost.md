# Scarlet gateway with Bifrost OSS

The public safety boundary is `backend/cmd/ai-gateway`. It authenticates callers,
consults Scarlet governance, then executes the existing `pkg/aisecurity` runtime.
Only model inference passes through the private Bifrost process. RAG document
retrieval and approved MCP actions retain the existing runtime's ACL/lease,
integrity, definition-pin, independent approval, nonce and audit enforcement.
This does not enable Bifrost's automatic Agent loop or replace those controls.

The fixed OSS release is `transports/v2.5.1`, source commit
`7cb75fb2f8276ca48d0792c857fecb67b8879e2f`, Apache-2.0. The image and digest are
recorded in [version.json](../integrations/bifrost/version.json). This is an OSS
integration; it does not include Bifrost Enterprise guardrails, SSO or clustering.

## Supported public profile

`POST /v1/chat/completions` accepts only `model`, `messages`, `stream`,
`max_tokens`, `temperature`, the restricted `stream_options`, and reviewed
`modalities`/`audio` settings. Messages contain `role` and string `content`;
roles are system/user/assistant. With an explicitly configured media policy,
content can instead be text, inline PNG/JPEG or PCM WAV parts. Existing runtime
limits apply: 128 messages, output token setting 1–4096 (zero/absent defaults to
1024), temperature 0–2, bounded UTF-8 JSON, no duplicate keys or unknown fields.
Tools/function calls, remote media URLs, arbitrary formats, embeddings,
Responses API, realtime, caller-selected providers/fallbacks and unreviewed
response-format extensions are rejected. This is a restricted chat profile,
not full API parity.

`stream:true` consumes actual upstream SSE, requires a complete terminal result
and usage frame, assembles all deltas, then checks the complete output before
replaying SSE (`X-Scarlet-Stream-Mode: strict_hold`). This prevents splitting a
secret or blocked pattern across deltas from bypassing inspection. It deliberately
buffers the full response and provides no low-latency token-release claim.
Authorization is checked during ingestion and before each replay event. Interrupted,
malformed or unsupported streams fail closed; multimedia output deltas are not
supported. Checked image/audio inputs may produce a checked text stream.

Media requires a ready trusted OCR/ASR processor and `model.media` policy. PNG/JPEG
pixels are re-encoded to PNG without metadata; WAV must be mono 16 kHz 16-bit PCM
and at most 30 seconds. Actual normalized bytes are hashed and inspected. OCR/ASR
text enters the existing input/output rules; category permissions must also allow
`image`/`audio`, and `unclassified_media` for blank or low-confidence recognition.
This is text recognition, not a claim to recognize all visual meaning or hidden
information. Unsupported formats, missing scanners and unbound receipts are denied.
Image/audio outputs use checked content parts or `message.audio`. Media output is
non-streaming and is excluded from response caching. See [scanner deployment](ai-gateway-media.md).

The public `model` is a Scarlet catalog ID. The runtime injects its trusted
`upstreamModel` and selects the fixed Bifrost endpoint. Bifrost then uses one
configured OpenAI-compatible destination and one provider key per private process;
the profile file may contain an array of up to eight independently bound processes.
Every enabled catalog model must match exactly one process, provider, region and
upstream model name. The result contains a normal chat-completion envelope with
one assistant choice. Provider usage is omitted:
the current runtime does not preserve independently verified provider accounting.

Use `Idempotency-Key` (16–80 characters matching the runtime request-ID syntax) for
a stable execution nonce. Only an omitted header generates a fresh nonce; explicit
empty or duplicate values are rejected, including identical duplicates. Optional
`X-Scarlet-Issued-At`/`X-Scarlet-Expires-At` must each contain a single nonempty
RFC3339 timestamp, validated by the runtime; invalid explicit values never become
a new validity window. Authentication and JSON content-type headers must also be
unambiguous, and duplicate content-encoding headers are rejected. Ordinary callers
receive a validity window of at most 60 seconds. Retries
with an already consumed nonce are refused; a new nonce is a new operation.

`POST /v1/execute` preserves the explicit runtime request contract, including RAG.
It passes through the same gateway governance gate as text inference and tools.
The MCP facade at `/mcp` implements authenticated `server/discover`, `tools/list`
and `tools/call` in the 2026-07-28 JSON profile, including string/integer RPC IDs
and protocol metadata/header checks. It rejects legacy initialize/session flows
and GET/SSE transport; it does not provide arbitrary transport/version compatibility
or an autonomous Agent loop. The call-only adapter at `/v1/mcp/execute` is an explicit Scarlet
extension, not a generic MCP endpoint: it requires a long string RPC ID plus
`params._meta.scarlet.{issuedAt,expiresAt,approvalId?}` and explicitly registered
tool names. The catalog target argument is extracted as the independently
authorized runtime `target`; callers cannot inject an upstream address or identity.

## Trusted deployment inputs

The CLI requires private `-catalog`, `-bindings`, `-bifrost-profile`, `-audit` and
`-journal` paths; `-documents` provides the private local RAG snapshot. The catalog
is the existing approved `aisecurity.Config`, not a replacement governance file.
Bindings contain `credentials` (synthetic/offline tests only), fixed `targets`,
and `tools: [{name,toolId,action}]`. A target is `{url,authorizationEnv?}`.

The Bifrost profile has these fields:

| Field | Contract |
| --- | --- |
| `provider`, `region` | Trusted business/provider declarations; must equal every enabled catalog model and controller grant. Not independent geographic attestation. |
| `endpointRef` | `env:SCARLET_GATEWAY_…`; resolved locally to the fixed provider base URL and checked against the grant. |
| `baseUrl` | Optional explicit value; if present it must equal the referenced environment value. Production requires HTTPS without userinfo, query or fragment. |
| `apiKeyEnv` | Name of the one provider credential environment variable. The controller's `secretRef` must equal `env:` plus this name. |
| `models` | Exact reviewed upstream model names; no wildcard, regex, provider-prefix or dynamic alias syntax. |
| `port` | Private Bifrost loopback port, 1024–65535. |
| `privateEndpointApproved` | Default false. Explicit approval to use this profile's fixed HTTPS private endpoint; does not permit caller-selected URLs. |

Each enabled catalog model requires `responseFormat: openai_chat`, the matching
provider/region, an upstream name in the profile allowlist, and an `endpointRef`
whose runtime target URL is exactly
`http://127.0.0.1:<port>/openai/v1/chat/completions`. This is checked again on every
catalog reload. Target/model drift fails closed. The catalog digest binds the
controller's approved policy snapshot; changing environment values or a private
profile remains a trusted deployment action requiring deployment review.

Bifrost's native `network_config.allow_private_network` is enabled for synthetic
tests or an explicitly approved fixed private endpoint. That field is a boolean,
not a CIDR policy. This release does not claim a Bifrost CIDR egress allowlist.
Private access is constrained by the single operator-controlled base URL, TLS,
no caller forwarding headers, and refusal of redirects. DNS and provider-side
routing still belong to the trusted deployment boundary. `-local` allows only a literal loopback public
listener and test HTTP provider hosts; it is not a production switch.

## Governed cache and cross-provider failover

The managed process receives generated configuration, not arbitrary Bifrost
resources. Its response cache, prompt plugin and local content logging are disabled;
no virtual model, fallback or Agent resources are configured. Provider
`max_retries` is zero and there is exactly one provider key (permanent-key rotation
can walk a pool even at zero retries). Caller Bifrost cache/direct-key/routing
headers are never forwarded. The original prompt is checked before Bifrost, and
the actual completed result is checked before release, including conservative
trusted document category taint.

Scarlet's optional exact-response cache lives outside Bifrost. It stores normalized,
checked text only, encrypted with AES-256-GCM and scoped to workspace, application,
principal, actual route, complete input, document scope, catalog/policy, inspection
version and key version. TTL, LRU entry/byte limits and owner invalidation apply.
Every hit consumes a fresh execution nonce, rechecks current authorization and
output policy, and writes an independent audit event before release. It creates no
provider reservation. Invalidation or configuration changes prevent stale writeback.

`fallbackModelIds` are an administrator-approved ordered list of catalog IDs on a
provider route. Every actual route independently checks identity, region, media/
data categories, catalog and budget, and obtains its own reservation. Each may use
a different upstream model name and provider process. Only explicit HTTP
429/502/503/504 results before output release can trigger failover. Network timeouts,
unknown transport results, data-policy denials and tool/MCP actions are not retried.
Unknown charges retain their original reservation; a new route never implies the
previous attempt was free. Shared circuit state can skip an unhealthy route.

Bifrost v2.5.1 adds `extra_fields` to its OpenAI wire response. Only the trusted
fixed-profile gateway context discards that known bounded metadata field. The
ordinary `openai_chat` runtime adapter remains unchanged: unknown response fields,
wrong reported model, tool calls, unsupported media and semantic error bodies are
still rejected. Metadata, including any embedded raw response, is not returned or
included in content-free audit. This verifies the returned model declaration; it
does not attest the model's implementation inside a remote provider.

## Governance and accounting

Production requires a fixed HTTPS `-control-url`, private `-control-token-file`
and `-workspace`. Node bearer credentials are controller-bound to a workspace.
Only the SHA-256 client token hash goes to `/resolve`; client plaintext tokens
are not sent to the controller. Newly issued/revoked dashboard credentials are
looked up dynamically. Controller failure never falls back to static credentials.
The runtime rechecks identity before upstream execution and before output release.
The same checks compare the reservation's `configRevision`; changing governance
scope during an operation suppresses output even if the principal is unchanged.
The revision is workspace-wide, so unrelated edits may suppress in-flight calls.

`BeforeExecute` obtains a durable controller reservation bound to request/catalog
digests and trusted identity. The grant's model/provider/region/format and endpoint/
secret references must match the local fixed profile; it cannot redirect execution.
A replayed authorization is refused across nodes, independently of the local
nonce journal. An uncertain authorization submission is not automatically retried.

Quotes use actual serialized input bytes as conservative input units and the
requested output-token ceiling. They are fixed policy quotes in USD micro-units,
not the provider's measured tokens or invoice. RAG/tool calls have no model quote
but still require governance permission and request-rate capacity. Settlement uses
the real runtime audit: only terminal completion, or a definite denied decision,
can settle. Allowed decisions/prepared events never refund. Successful attempted
calls charge the reserved quote; definite non-attempts release it; attempted failures,
timeouts and output suppression retain uncertain reservations for reconciliation.
Audit/controller failure after execution suppresses output and never claims rollback.
Authenticated denied runtime decisions without a reservation (for example input
DLP or missing tool approval) are sent through the workspace-bound `/events`
machine API. Those events are idempotent audit only and have no accounting effect.
Protocol parsing/authentication failures that never form a runtime decision are
not claimed as completed security evaluations.
See [enterprise governance](ai-gateway-enterprise.md) for controller configuration.

## Build and deployment

`docker/bifrost/Dockerfile` builds Scarlet from this workspace and copies it into
the fixed official Bifrost image. It does not add Bifrost dependencies to the root
Go module. The CLI starts and supervises `/app/main` with a private generated
configuration, waits for bounded HTTP readiness and authenticated current-version
media probes required by the enabled catalog, then opens the external HTTPS
listener. Bifrost binds only loopback and has no published management/proxy port.
The CLI sends workspace-scoped heartbeats every 15 seconds after HTTP readiness.
`-node-id` defaults to the validated hostname; use distinct explicit IDs for
multiple processes on one host. Healthy means process/dependency readiness,
not model quality, provider SLA or a successful customer inference test.
Controller outages leave the node stale instead of manufacturing new health;
shutdown sends degraded best effort.
The private loopback health listener defaults to `127.0.0.1:8906`. `/healthz`
is process liveness; `/readyz` is a fresh dependency result (204 or 503).
`ai-gateway -check-ready` reads this result without opening bindings, credentials
or the nonce journal. It does not scan content or reserve budget. Do not publish
this port. Native instances on one host need separate `-health-listen` ports.
Media probes use fixed blank pixels and silent PCM, have a 50-second deadline,
and run serially with a 30-second interval after each completion. Results older
than 90 seconds are unready. Busy scanner slots can conservatively degrade the
node; recovery never deletes the journal or releases unresolved charges.
See [media operations](ai-gateway-media.md) for the synchronized v2 compose
overlay, scanner-only CA trust, historical v1 metadata and acceptance boundaries.
Its child environment contains only the provider credential and required PATH/CA
settings, not controller credentials, database DSNs or unrelated application secrets.

Use `compose.ai-gateway.yaml` with reviewed files under `var/ai-gateway/config`
and durable private state under `var/ai-gateway/state`. The compose file deliberately
has no working demo credentials. TLS certificates and the node token must be supplied.
Production CLI startup requires managed Bifrost and remote governance; an external
already-running Bifrost is accepted only in loopback synthetic mode.

Verification commands:

```sh
go -C backend test -race ./pkg/aisecurity ./cmd/ai-gateway ./cmd/ai-security-agent
go -C backend vet ./pkg/aisecurity ./cmd/ai-gateway ./cmd/ai-security-agent
python3 docker/bifrost/e2e.py
SCARLET_DB_DSN="$PLUME_STORE_TEST_DSN" python3 docker/bifrost/e2e-v3.py
python3 docker/bifrost/e2e-media.py \
  --tesseract "$TESSERACT" --whisper "$WHISPER" \
  --whisper-model "$WHISPER_MODEL" \
  --whisper-model-sha256 "$WHISPER_MODEL_SHA256" --wav "$SYNTHETIC_WAV"
docker build -f docker/bifrost/Dockerfile -t scarlet-ai-gateway:bifrost-2.5.1-media-v2 .
```

The Docker test uses the real pinned OSS image with a synthetic local provider;
there are no paid model calls. Controller integration acceptance is provided by
`docker/bifrost/e2e-controller.py`, using the real controller/PostgreSQL and the
same fixed Bifrost image. Customer TLS, IdP mapping, provider behavior and production
network acceptance remain separate from synthetic acceptance.
