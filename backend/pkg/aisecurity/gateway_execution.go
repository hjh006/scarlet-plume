package aisecurity

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/hjh006/scarlet-plume/backend/pkg/gatewaygovernance"
)

type gatewayBeforeKey struct{}
type gatewayLiveRevisionKey struct{}
type gatewayOpenModelsKey struct{}
type gatewayFallbackKey struct{}
type gatewayFallbackChain struct {
	ParentRequestID, RootModelID, PreviousReservationID string
	Attempt                                             int
}

func (g *gatewayHandler) beforeRuntime(ctx context.Context, r Request, d Decision) error {
	if g.before == nil {
		return nil
	}
	inputBytes, maxOutput := int64(0), int64(0)
	if r.Kind == "model" {
		var payload struct {
			MaxTokens int64 `json:"max_tokens"`
		}
		_ = json.Unmarshal([]byte(r.Input), &payload)
		maxOutput = payload.MaxTokens
		if maxOutput == 0 {
			maxOutput = 1024
		}
		inputBytes = int64(len(r.Input))
		if r.sourceModelInput != "" {
			inputBytes = int64(len(r.sourceModelInput))
		}
		if r.modelWireBytes > 0 {
			inputBytes = r.modelWireBytes
		}
	}
	hash, _ := ctx.Value(gatewayCredentialHashKey{}).(string)
	fact := GatewayAuthorization{Identity: Identity{ApplicationID: r.ApplicationID, PrincipalID: r.PrincipalID}, TokenHash: hash, Kind: r.Kind, RequestID: r.RequestID, ModelID: r.ModelID, ToolID: r.ToolID, Action: r.Action, Target: r.Target, RequestDigest: d.RequestDigest, CatalogDigest: d.CatalogDigest, InputBytes: inputBytes, MaxOutputTokens: maxOutput}
	fact.TaskContext = taskBinding(ctx, r)
	if r.Kind == "tool" && d.RequiresApproval {
		fact.ApprovalID = r.ApprovalID
	}
	fact.SourceDigests = sourceDigestList(r)
	if r.mediaInputProjection != nil {
		fact.ProjectionBytes = int64(len(*r.mediaInputProjection))
		fact.MediaBytes = r.mediaInputBytes
		fact.MediaDigests = append([]string(nil), r.mediaDigests...)
		fact.MediaInspectionVersion = MediaInspectionVersion
	}
	if chain, ok := ctx.Value(gatewayFallbackKey{}).(*gatewayFallbackChain); ok && r.Kind == "model" {
		fact.ParentRequestID = chain.ParentRequestID
		fact.RootModelID = chain.RootModelID
		fact.PreviousReservationID = chain.PreviousReservationID
		fact.Attempt = chain.Attempt
	}
	return g.before(ctx, fact)
}
func retryableGatewayResult(b *gatewayBuffer) bool {
	if b.status != 502 {
		return false
	}
	var result struct {
		Decision  Decision  `json:"decision"`
		Execution Execution `json:"execution"`
	}
	if strictJSON(bytes.NewReader(b.body.Bytes()), &result) != nil || result.Decision.Code != "upstream_non_success_or_redirect" || !result.Execution.Attempted || result.Execution.OutputReleased {
		return false
	}
	return result.Execution.HTTPStatus == 429 || result.Execution.HTTPStatus == 502 || result.Execution.HTTPStatus == 503 || result.Execution.HTTPStatus == 504
}
func newGatewayCallID() string {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(nonce[:])
}

type gatewayReleaseContext struct {
	context.Context
	values context.Context
}

func (c gatewayReleaseContext) Value(key any) any { return c.values.Value(key) }
func (g *gatewayHandler) checkedOutputCurrent(ctx context.Context, b *gatewayBuffer) bool {
	if b == nil || b.checkedOutput == nil || b.checkedOutput.values == nil || ctx.Err() != nil {
		return false
	}
	// Internal task-watch cancellation ends with ServeHTTP, but its trusted
	// values belong to the held result. Only the current outer request controls
	// cancellation/deadlines while rechecking that original execution identity.
	release := gatewayReleaseContext{Context: ctx, values: b.checkedOutput.values}
	return g.runtime.fresh(release, b.checkedOutput.request, b.checkedOutput.decision) && ctx.Err() == nil
}

// replayModelStream releases the held checked text under its original execution
// context. Recheck before every event and [DONE]; never acquire new source or
// media observations to authorize an older output.
func (g *gatewayHandler) replayModelStream(w http.ResponseWriter, in *http.Request, b *gatewayBuffer, output, finish, publicModel, publicRequestID string) {
	fresh := func() bool { return g.checkedOutputCurrent(in.Context(), b) }
	if !fresh() {
		gatewayError(w, 403, "authorization_changed_before_stream_release")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Scarlet-Stream-Mode", "strict_hold")
	send := func(delta map[string]string, reason any) error {
		if !fresh() {
			return errors.New("authorization changed")
		}
		raw, _ := json.Marshal(map[string]any{"id": "chatcmpl-" + publicRequestID, "object": "chat.completion.chunk", "created": g.runtime.options.Now().Unix(), "model": publicModel, "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": reason}}})
		if _, err := w.Write(append(append([]byte("data: "), raw...), []byte("\n\n")...)); err != nil {
			return err
		}
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		return nil
	}
	if send(map[string]string{"role": "assistant"}, nil) != nil {
		return
	}
	for len(output) > 0 {
		n := min(len(output), 1024)
		for n > 0 && n < len(output) && !utf8.RuneStart(output[n]) {
			n--
		}
		if n == 0 {
			return
		}
		if send(map[string]string{"content": output[:n]}, nil) != nil {
			return
		}
		output = output[n:]
	}
	if send(map[string]string{}, finish) != nil || !fresh() {
		return
	}
	_, _ = w.Write([]byte("data: [DONE]\n\n"))
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func (g *gatewayHandler) execute(w http.ResponseWriter, in *http.Request, identity Identity, wire RuntimeRequest, chat bool) {
	selected, err := selectedTask(in, wire.TaskID)
	if err != nil {
		gatewayError(w, 400, "task_selection_invalid")
		return
	}
	wire.TaskID = selected
	c, err := g.runtime.options.Catalog(in.Context())
	if err != nil {
		gatewayError(w, 503, "catalog_unavailable")
		return
	}
	root := wire
	chain := &gatewayFallbackChain{ParentRequestID: root.RequestID, RootModelID: root.ModelID}
	ctx := context.WithValue(in.Context(), gatewayFallbackKey{}, chain)
	if root.TaskID != "" {
		ctx = context.WithValue(ctx, gatewayTaskExecutionKey{}, &gatewayTaskExecution{Binding: gatewaygovernance.TaskContext{TaskID: root.TaskID, TaskStepID: root.RequestID, TaskStepDigest: RequestDigest(requestFromWire(root, identity)), LeaseProtocolVersion: gatewaygovernance.TaskLeaseProtocolVersion}})
	}
	openModels := map[string]bool{}
	ctx = context.WithValue(ctx, gatewayOpenModelsKey{}, openModels)
	var liveRevision int64
	ctx = context.WithValue(ctx, gatewayLiveRevisionKey{}, &liveRevision)
	ctx = context.WithValue(ctx, gatewayCredentialHashKey{}, tokenHash(strings.TrimPrefix(in.Header.Get("Authorization"), "Bearer ")))
	in = in.WithContext(ctx)
	attempts := []string{root.ModelID}
	if wire.Kind == "model" {
		var fallbacks []string
		if g.fallbackModels != nil {
			fallbacks, err = g.fallbackModels(ctx, root.ModelID)
			if err != nil {
				gatewayError(w, 503, "fallback_scope_unavailable")
				return
			}
		} else {
			fallbacks = g.fallbacks[root.ModelID]
		}
		if len(fallbacks) > 3 {
			gatewayError(w, 503, "fallback_chain_invalid")
			return
		}
		seen := map[string]bool{root.ModelID: true}
		for _, id := range fallbacks {
			if !validID(id) || seen[id] {
				gatewayError(w, 503, "fallback_chain_invalid")
				return
			}
			seen[id] = true
			attempts = append(attempts, id)
		}
	}
	var buffer *gatewayBuffer
	var actual RuntimeRequest
	for attempt, modelID := range attempts {
		if openModels[modelID] && attempt < len(attempts)-1 {
			continue
		}
		actual = root
		chain.Attempt = attempt
		if attempt > 0 {
			actual.ModelID = modelID
			actual.RequestID = "fallback-" + hash(root.RequestID+"\x00"+modelID)
		}
		callID := newGatewayCallID()
		if callID == "" {
			gatewayError(w, 503, "call_reference_unavailable")
			return
		}
		attemptIn := in.WithContext(context.WithValue(in.Context(), gatewayCallIDKey{}, callID))
		buffer = &gatewayBuffer{header: http.Header{}}
		g.executeAttempt(buffer, attemptIn, identity, actual, c)
		// Keep the actual call context for fresh credential checks during replay.
		in = attemptIn
		if buffer.status == 200 || wire.Kind != "model" || !retryableGatewayResult(buffer) {
			break
		}
	}
	if !chat {
		for name, values := range buffer.header {
			w.Header()[name] = values
		}
		if buffer.status >= 200 && buffer.status < 300 {
			if !g.checkedOutputCurrent(in.Context(), buffer) {
				gatewayError(w, 403, "authorization_changed_before_output_release")
				return
			}
			if outer, ok := w.(*gatewayBuffer); ok {
				outer.checkedOutput = buffer.checkedOutput
			}
		}
		w.WriteHeader(buffer.status)
		_, _ = w.Write(buffer.body.Bytes())
		return
	}
	if buffer.status != 200 {
		gatewayError(w, buffer.status, "runtime_execution_denied")
		return
	}
	var response struct {
		Model        string        `json:"model"`
		Output       string        `json:"output"`
		FinishReason string        `json:"finishReason"`
		Media        []MediaOutput `json:"media,omitempty"`
	}
	if strictJSON(bytes.NewReader(buffer.body.Bytes()), &response) != nil || response.FinishReason != "stop" && response.FinishReason != "length" {
		gatewayError(w, 502, "normalized_model_response_required")
		return
	}
	if buffer.header.Get("X-Scarlet-Cache") != "" {
		w.Header().Set("X-Scarlet-Cache", buffer.header.Get("X-Scarlet-Cache"))
	}
	if stream, e := ModelRequestStream(root.Input); e == nil && stream {
		if len(response.Media) > 0 {
			gatewayError(w, 422, "stream_media_output_unsupported")
			return
		}
		g.replayModelStream(w, in, buffer, response.Output, response.FinishReason, root.ModelID, root.RequestID)
		return
	}
	message := map[string]any{"role": "assistant", "content": response.Output}
	parts := []any{map[string]string{"type": "text", "text": response.Output}}
	audioCount := 0
	for _, media := range response.Media {
		switch media.Type {
		case "image_url":
			parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]string{"url": "data:" + media.MIMEType + ";base64," + media.Data}})
		case "input_audio", "audio":
			audioCount++
			if audioCount > 1 {
				gatewayError(w, 422, "one_checked_audio_output_required")
				return
			}
			message["audio"] = map[string]any{"id": media.Digest, "data": media.Data, "expires_at": g.runtime.options.Now().Unix() + 60, "transcript": response.Output}
		default:
			gatewayError(w, 422, "checked_media_output_type_invalid")
			return
		}
	}
	if len(parts) > 1 {
		message["content"] = parts
	}
	if !g.checkedOutputCurrent(in.Context(), buffer) {
		gatewayError(w, 403, "authorization_changed_before_output_release")
		return
	}
	if outer, ok := w.(*gatewayBuffer); ok {
		outer.checkedOutput = buffer.checkedOutput
	}
	respond(w, 200, map[string]any{"id": "chatcmpl-" + root.RequestID, "object": "chat.completion", "created": g.runtime.options.Now().Unix(), "model": root.ModelID, "choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": response.FinishReason}}})
}
