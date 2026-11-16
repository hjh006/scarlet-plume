package aisecurity

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hjh006/scarlet-plume/backend/pkg/gatewaygovernance"
)

type GatewayControllerOptions struct {
	URL           string
	Token         string
	WorkspaceSlug string
	Profile       BifrostProfile
	Profiles      []BifrostProfile
	Catalog       func(context.Context) (Config, error)
	AllowLoopback bool
}

type GatewayController struct {
	options      GatewayControllerOptions
	client       *http.Client
	mu           sync.Mutex
	reservations map[string]gatewayReservation
	capabilities []string
}
type gatewayReservation struct {
	ID, Digest string
	Revision   int64
}

func NewGatewayController(o GatewayControllerOptions) (*GatewayController, error) {
	if !validTarget(o.URL, o.AllowLoopback) || len(o.Token) < 32 || len(o.Token) > 512 || strings.ContainsAny(o.Token, "\r\n") || !validID(o.WorkspaceSlug) || o.Catalog == nil {
		return nil, errors.New("invalid fixed gateway controller")
	}
	return &GatewayController{options: o, client: &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, DisableCompression: true, ResponseHeaderTimeout: 3 * time.Second, MaxResponseHeaderBytes: 16384}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, reservations: map[string]gatewayReservation{}}, nil
}

func (c *GatewayController) post(ctx context.Context, operation string, input, output any) error {
	raw, _ := json.Marshal(input)
	request, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(c.options.URL, "/")+"/api/v1/internal/ai-gateway/"+operation, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+c.options.Token)
	request.Header.Set("Content-Type", "application/json")
	response, err := c.client.Do(request)
	if err != nil {
		return errors.New("gateway controller result unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 || response.Header.Get("Content-Encoding") != "" {
		return errors.New("gateway controller rejected request: HTTP " + strconv.Itoa(response.StatusCode))
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
	if err != nil || len(body) > 2<<20 {
		return errors.New("gateway controller result incomplete")
	}
	if output == nil {
		if len(body) != 0 && !json.Valid(body) {
			return errors.New("gateway controller result invalid")
		}
		return nil
	}
	if strictJSON(bytes.NewReader(body), output) != nil {
		return errors.New("gateway controller result invalid")
	}
	return nil
}

type gatewayResolved struct {
	RequireTask    bool                `json:"requireTask,omitempty"`
	CredentialID   int64               `json:"credentialId"`
	ConfigRevision int64               `json:"configRevision"`
	ToolIDs        []string            `json:"toolIds"`
	CatalogDigest  string              `json:"catalogDigest"`
	Fallbacks      map[string][]string `json:"fallbacks"`
	OpenModelIDs   []string            `json:"openModelIds"`
	Identity
}

func (c *GatewayController) resolve(ctx context.Context, hash string) (gatewayResolved, error) {
	var resolved gatewayResolved
	err := c.post(ctx, "resolve", struct {
		WorkspaceSlug string `json:"workspaceSlug"`
		TokenHash     string `json:"tokenHash"`
	}{c.options.WorkspaceSlug, hash}, &resolved)
	if err != nil || resolved.CredentialID <= 0 || resolved.ConfigRevision <= 0 || !validID(resolved.ApplicationID) || !validID(resolved.PrincipalID) {
		return gatewayResolved{}, errors.New("gateway credential unknown or unavailable")
	}
	return resolved, nil
}

func (c *GatewayController) Authenticate(ctx context.Context, hash string) (Identity, error) {
	resolved, err := c.resolve(ctx, hash)
	if err != nil {
		return Identity{}, err
	}
	// ponytail: workspace-wide revision deliberately suppresses unrelated in-flight
	// calls too; per-resource revisions can narrow this when availability requires it.
	callID, _ := ctx.Value(gatewayCallIDKey{}).(string)
	c.mu.Lock()
	reservation, exists := c.reservations[callID]
	c.mu.Unlock()
	if live, ok := ctx.Value(gatewayLiveRevisionKey{}).(*int64); ok {
		c.mu.Lock()
		if *live == 0 {
			*live = resolved.ConfigRevision
		}
		changed := *live != resolved.ConfigRevision
		c.mu.Unlock()
		if changed {
			return Identity{}, errors.New("gateway governance scope changed")
		}
	}
	if exists && reservation.Revision != resolved.ConfigRevision {
		return Identity{}, errors.New("gateway governance scope changed")
	}
	return resolved.Identity, nil
}

func (c *GatewayController) DiscoverTools(ctx context.Context, identity Identity, tokenHash, catalogDigest string) ([]string, error) {
	resolved, err := c.resolve(ctx, tokenHash)
	if err != nil || resolved.Identity != identity || resolved.CatalogDigest != catalogDigest || !ids(resolved.ToolIDs) {
		return nil, errors.New("gateway live tool scope unavailable")
	}
	if resolved.RequireTask {
		if task, _ := ctx.Value(gatewayTaskSelectionKey{}).(string); task == "" {
			return nil, errors.New("task required for discovery")
		}
	}
	return append([]string{}, resolved.ToolIDs...), nil
}

func (c *GatewayController) Heartbeat(ctx context.Context, nodeID, status string, failures int) error {
	if !validID(nodeID) || status != "healthy" && status != "degraded" || failures < 0 || failures > 1000000 {
		return errors.New("invalid gateway heartbeat")
	}
	return c.post(ctx, "heartbeat", struct {
		WorkspaceSlug string   `json:"workspaceSlug"`
		ID            string   `json:"id"`
		Status        string   `json:"status"`
		Failures      int      `json:"failures"`
		Capabilities  []string `json:"capabilities"`
		Version       string   `json:"version"`
	}{c.options.WorkspaceSlug, nodeID, status, failures, c.capabilities, "scarlet-gateway-v4"}, nil)
}

func (c *GatewayController) BeforeExecute(ctx context.Context, fact GatewayAuthorization) error {
	callID, _ := ctx.Value(gatewayCallIDKey{}).(string)
	if callID == "" {
		return errors.New("gateway call context missing")
	}
	c.mu.Lock()
	full := len(c.reservations) >= 10000
	c.mu.Unlock()
	if full {
		return errors.New("gateway reservation reconciliation required")
	}
	var grant struct {
		gatewaygovernance.TaskContext
		ApprovalID             string   `json:"approvalId,omitempty"`
		SourceDigests          []string `json:"sourceDigests,omitempty"`
		ProjectionBytes        int64    `json:"projectionBytes,omitempty"`
		MediaBytes             int64    `json:"mediaBytes,omitempty"`
		MediaDigests           []string `json:"mediaDigests,omitempty"`
		MediaInspectionVersion string   `json:"mediaInspectionVersion,omitempty"`
		Identity
		Region                string   `json:"region"`
		ResponseFormat        string   `json:"responseFormat"`
		RequestID             string   `json:"requestId"`
		RequestDigest         string   `json:"requestDigest"`
		CatalogDigest         string   `json:"catalogDigest"`
		Kind                  string   `json:"kind"`
		CredentialID          int64    `json:"credentialId"`
		ReservationID         string   `json:"reservationId"`
		ConfigRevision        int64    `json:"configRevision"`
		ModelID               string   `json:"modelId"`
		ProviderID            string   `json:"providerId"`
		Provider              string   `json:"provider"`
		UpstreamModel         string   `json:"upstreamModel"`
		EndpointRef           string   `json:"endpointRef"`
		SecretRef             string   `json:"secretRef"`
		MaxCostMicros         int64    `json:"maxCostMicros"`
		MaxTokens             int64    `json:"maxTokens"`
		Replayed              bool     `json:"replayed"`
		FallbackModelIDs      []string `json:"fallbackModelIds"`
		ParentRequestID       string   `json:"parentRequestId,omitempty"`
		RootModelID           string   `json:"rootModelId,omitempty"`
		PreviousReservationID string   `json:"previousReservationId,omitempty"`
		Attempt               int      `json:"attempt,omitempty"`
	}
	input := struct {
		WorkspaceSlug string `json:"workspaceSlug"`
		GatewayAuthorization
	}{c.options.WorkspaceSlug, fact}
	if c.post(ctx, "authorize", input, &grant) != nil {
		return errors.New("gateway authorization unavailable")
	}
	// A repeated authorization must never attach an old reservation to this
	// attempt: its denial audit must not refund a possibly executed old call.
	if grant.Replayed {
		return errors.New("gateway authorization replayed")
	}
	if grant.ReservationID == "" || grant.RequestDigest != fact.RequestDigest || grant.ConfigRevision <= 0 {
		return errors.New("gateway reservation binding invalid")
	}
	if chain, ok := ctx.Value(gatewayFallbackKey{}).(*gatewayFallbackChain); ok {
		chain.PreviousReservationID = grant.ReservationID
	}
	c.mu.Lock()
	c.reservations[callID] = gatewayReservation{grant.ReservationID, fact.RequestDigest, grant.ConfigRevision}
	c.mu.Unlock()
	if !slices.Equal(grant.SourceDigests, fact.SourceDigests) || grant.TaskContext != fact.TaskContext || grant.ApprovalID != fact.ApprovalID || grant.Identity != fact.Identity || grant.Kind != fact.Kind || grant.RequestID != fact.RequestID || grant.CatalogDigest != fact.CatalogDigest || grant.ModelID != fact.ModelID {
		return errors.New("gateway authorization scope mismatch")
	}
	if fact.Kind == "model" {
		catalog, err := c.options.Catalog(ctx)
		if err != nil || ConfigDigest(catalog) != fact.CatalogDigest {
			return errors.New("gateway catalog changed")
		}
		var model Model
		for _, v := range catalog.Models {
			if v.ID == fact.ModelID {
				model = v
			}
		}
		actual := model.UpstreamModel
		if actual == "" {
			actual = model.ID
		}
		p := c.options.Profile
		if len(c.options.Profiles) > 0 {
			p = BifrostProfile{}
			for _, profile := range c.options.Profiles {
				if profile.Provider == model.Provider && profile.Region == model.Region && profile.EndpointRef == grant.EndpointRef {
					p = profile
					break
				}
			}
		}
		if grant.ProviderID == "" || grant.Provider != model.Provider || grant.Provider != p.Provider || grant.Region != model.Region || grant.Region != p.Region || grant.UpstreamModel != actual || grant.ResponseFormat != model.ResponseFormat || grant.EndpointRef != p.EndpointRef || grant.SecretRef != "env:"+p.APIKeyEnv {
			return errors.New("gateway fixed provider scope mismatch")
		}
	}
	return nil
}

func (c *GatewayController) Audit(ctx context.Context, a AuditRecord) error {
	terminal := a.Stage == "completed" || a.Stage == "decision" && a.Decision.Outcome != "allow"
	if !terminal {
		return nil
	}
	callID, _ := ctx.Value(gatewayCallIDKey{}).(string)
	c.mu.Lock()
	reservation, ok := c.reservations[callID]
	c.mu.Unlock()
	if !ok {
		if !a.Execution.Attempted && (a.Stage == "decision" && a.Decision.Outcome != "allow" || a.Stage == "completed" && a.Execution.CacheHit && a.CacheEvidence != nil) {
			token, _ := ctx.Value(gatewayCredentialHashKey{}).(string)
			var revision int64
			modelID := ""
			if a.Execution.CacheHit {
				if current, ok := ctx.Value(gatewayLiveRevisionKey{}).(*int64); ok {
					revision = *current
				}
				modelID = a.CacheEvidence.ModelID
			} else {
				token = ""
			}
			chain := &gatewayFallbackChain{}
			if a.Execution.CacheHit {
				if current, ok := ctx.Value(gatewayFallbackKey{}).(*gatewayFallbackChain); ok {
					chain = current
				}
			}
			return c.post(ctx, "events", struct {
				gatewaygovernance.TaskContext
				WorkspaceSlug         string      `json:"workspaceSlug"`
				Audit                 AuditRecord `json:"audit"`
				TokenHash             string      `json:"tokenHash,omitempty"`
				ConfigRevision        int64       `json:"configRevision,omitempty"`
				ModelID               string      `json:"modelId,omitempty"`
				ParentRequestID       string      `json:"parentRequestId,omitempty"`
				RootModelID           string      `json:"rootModelId,omitempty"`
				PreviousReservationID string      `json:"previousReservationId,omitempty"`
				Attempt               int         `json:"attempt,omitempty"`
			}{taskContextFromAudit(ctx, a), c.options.WorkspaceSlug, a, token, revision, modelID, chain.ParentRequestID, chain.RootModelID, chain.PreviousReservationID, chain.Attempt}, nil)
		}
		return nil
	}
	if reservation.Digest != a.Decision.RequestDigest {
		return errors.New("gateway settlement digest mismatch")
	}
	input := struct {
		WorkspaceSlug string      `json:"workspaceSlug"`
		ReservationID string      `json:"reservationId"`
		RequestDigest string      `json:"requestDigest"`
		Audit         AuditRecord `json:"audit"`
	}{c.options.WorkspaceSlug, reservation.ID, reservation.Digest, a}
	if err := c.post(ctx, "settle", input, nil); err != nil {
		return err
	}
	c.mu.Lock()
	delete(c.reservations, callID)
	c.mu.Unlock()
	return nil
}

func (c *GatewayController) FallbackModels(ctx context.Context, rootModel string) ([]string, error) {
	token, _ := ctx.Value(gatewayCredentialHashKey{}).(string)
	resolved, err := c.resolve(ctx, token)
	if err != nil {
		return nil, err
	}
	if open, ok := ctx.Value(gatewayOpenModelsKey{}).(map[string]bool); ok {
		for _, id := range resolved.OpenModelIDs {
			open[id] = true
		}
	}
	return append([]string(nil), resolved.Fallbacks[rootModel]...), nil
}
func (c *GatewayController) cacheScope(ctx context.Context, s ModelCacheScope) gatewaygovernance.CacheScope {
	token, _ := ctx.Value(gatewayCredentialHashKey{}).(string)
	task, _ := ctx.Value(cacheTaskContextKey{}).(gatewaygovernance.TaskContext)
	return gatewaygovernance.CacheScope{TaskContext: task, WorkspaceSlug: c.options.WorkspaceSlug, TokenHash: token, ApplicationID: s.ApplicationID, PrincipalID: s.PrincipalID, ModelID: s.ModelID, CatalogDigest: s.CatalogDigest, InspectionVersion: s.InspectionVersion, InputHash: s.InputHash, DocumentHash: s.DocumentHash, PolicyHash: s.PolicyHash}
}
func (c *GatewayController) Get(ctx context.Context, s ModelCacheScope) ([]byte, bool, error) {
	var result gatewaygovernance.CacheResult
	if err := c.post(ctx, "cache/get", c.cacheScope(ctx, s), &result); err != nil {
		return nil, false, err
	}
	if !result.Hit {
		return nil, false, nil
	}
	if !digest.MatchString(result.OutputHash) || cacheTextDigest(result.Output) != result.OutputHash || len(result.Output) > 1<<20 {
		return nil, false, errors.New("cache integrity check failed")
	}
	return []byte(result.Output), true, nil
}
func (c *GatewayController) Put(ctx context.Context, s ModelCacheScope, raw []byte) error {
	chain, ok := ctx.Value(gatewayFallbackKey{}).(*gatewayFallbackChain)
	if !ok || chain.PreviousReservationID == "" {
		return errors.New("cache receipt missing")
	}
	return c.post(ctx, "cache/put", gatewaygovernance.CachePut{CacheScope: c.cacheScope(ctx, s), ReservationID: chain.PreviousReservationID, Output: string(raw), OutputHash: cacheTextDigest(string(raw))}, nil)
}

func (c *GatewayController) SetCapabilities(media, sources bool) {
	c.capabilities = []string{"streaming_strict_hold", "encrypted_exact_cache", "governed_fallback", "task_scope_and_kill"}
	if sources {
		c.capabilities = append(c.capabilities, "source_document_binding")
	}
	if media {
		c.capabilities = append(c.capabilities, "multimodal_projection")
	}
}
