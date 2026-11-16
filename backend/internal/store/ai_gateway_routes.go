package store

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/jackc/pgx/v5"
)

func gatewayValidateExtensions(c GatewayConfig) error {
	routes := map[string]GatewayProvider{}
	for _, p := range c.Providers {
		if len(p.FallbackModelIDs) > 3 {
			return ErrInvalid
		}
		seen := map[string]bool{}
		for _, id := range p.FallbackModelIDs {
			if !gatewayRuntimeID.MatchString(id) || id == p.ModelID || seen[id] {
				return ErrInvalid
			}
			seen[id] = true
		}
		if p.Enabled {
			routes[p.ModelID] = p
		}
	}
	var visit func(string, map[string]bool, int) bool
	visit = func(id string, path map[string]bool, depth int) bool {
		if path[id] || depth > 3 {
			return false
		}
		p, ok := routes[id]
		if !ok {
			return false
		}
		path[id] = true
		defer delete(path, id)
		for _, next := range p.FallbackModelIDs {
			if !visit(next, path, depth+1) {
				return false
			}
		}
		return true
	}
	for id := range routes {
		if !visit(id, map[string]bool{}, 0) {
			return ErrInvalid
		}
	}
	for _, a := range c.Applications {
		p := a.CachePolicy
		if p.Enabled && (p.TTLSeconds < 1 || p.TTLSeconds > 86400 || p.MaxEntries < 1 || p.MaxEntries > 10000 || p.MaxBytes < 1 || p.MaxBytes > 16777216 || !gatewayReference.MatchString(p.KeyRef) || !gatewayRuntimeID.MatchString(p.KeyVersion)) {
			return ErrInvalid
		}
		if p.KeyRef != "" && !gatewayReference.MatchString(p.KeyRef) || p.KeyVersion != "" && !gatewayRuntimeID.MatchString(p.KeyVersion) {
			return ErrInvalid
		}
		if a.Enabled {
			for _, id := range a.ModelIDs {
				for _, next := range routes[id].FallbackModelIDs {
					if !slices.Contains(a.ModelIDs, next) {
						return ErrInvalid
					}
				}
			}
		}
	}
	return nil
}

// The application account lock serializes admission across all nodes. A retry
// keeps its predecessor's unknown charge reserved and acquires its own quote.
func gatewayAuthorizeAttempt(ctx context.Context, tx pgx.Tx, ws int64, in GatewayAuthorizeInput, c GatewayConfig, provider GatewayProvider) error {
	routes := map[string]GatewayProvider{}
	for _, p := range c.Providers {
		if p.Enabled {
			routes[p.ModelID] = p
		}
	}
	isOpen := func(model string) (bool, error) {
		var open bool
		e := tx.QueryRow(ctx, `SELECT COALESCE(open_until>clock_timestamp(),false) FROM plume.ai_gateway_route_health WHERE workspace_id=$1 AND provider_id=$2`, ws, routes[model].ID).Scan(&open)
		if errors.Is(e, pgx.ErrNoRows) {
			return false, nil
		}
		return open, e
	}
	if in.Kind == "model" {
		open, e := isOpen(in.ModelID)
		if e != nil {
			return e
		}
		if open {
			return ErrGatewayCircuit
		}
	}
	if in.ParentRequestID == "" {
		if in.Attempt != 0 || in.RootModelID != "" || in.PreviousReservationID != "" {
			return ErrInvalid
		}
		return nil
	}
	if in.Kind != "model" || !gatewayText(in.ParentRequestID, 200) || !gatewayRuntimeID.MatchString(in.RootModelID) || in.Attempt < 0 || in.Attempt > 3 {
		return ErrInvalid
	}
	root := routes[in.RootModelID]
	if root.ID == "" {
		return ErrForbidden
	}
	chain := append([]string{root.ModelID}, root.FallbackModelIDs...)
	if len(chain) <= in.Attempt || chain[in.Attempt] != in.ModelID {
		return ErrForbidden
	}
	if in.Attempt == 0 {
		if in.PreviousReservationID != "" {
			return ErrInvalid
		}
		return nil
	}
	previousAttempt := -1
	if in.PreviousReservationID != "" {
		if len(in.PreviousReservationID) != 32 {
			return ErrInvalid
		}
		var app, principal, rootID, parent, catalog string
		var revision int64
		var raw []byte
		e := tx.QueryRow(ctx, `SELECT application_id,authorized->>'principalId',root_model_id,parent_request_id,attempt,last_audit,authorized->>'catalogDigest',(authorized->>'configRevision')::bigint FROM plume.ai_gateway_reservations WHERE workspace_id=$1 AND id=$2`, ws, in.PreviousReservationID).Scan(&app, &principal, &rootID, &parent, &previousAttempt, &raw, &catalog, &revision)
		if errors.Is(e, pgx.ErrNoRows) {
			return ErrForbidden
		}
		if e != nil {
			return e
		}
		if app != in.ApplicationID || principal != in.PrincipalID || rootID != in.RootModelID || parent != in.ParentRequestID || previousAttempt >= in.Attempt || catalog != in.CatalogDigest || revision != c.Revision || len(raw) == 0 {
			return ErrForbidden
		}
		var audit struct {
			Execution struct {
				Attempted      bool   `json:"attempted"`
				Outcome        string `json:"outcome"`
				HTTPStatus     int    `json:"httpStatus"`
				OutputReleased bool   `json:"outputReleased"`
			} `json:"execution"`
		}
		if json.Unmarshal(raw, &audit) != nil || !audit.Execution.Attempted || audit.Execution.Outcome != "failed" || audit.Execution.OutputReleased || !slices.Contains([]int{429, 502, 503, 504}, audit.Execution.HTTPStatus) {
			return ErrForbidden
		}
	} else {
		var exists bool
		if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM plume.ai_gateway_reservations WHERE workspace_id=$1 AND parent_request_id=$2)`, ws, in.ParentRequestID).Scan(&exists); e != nil {
			return e
		}
		if exists {
			return ErrConflict
		}
	}
	// A node can skip an earlier candidate only while the shared DB proves its
	// circuit is open. Reported local health cannot select a different route.
	for i := previousAttempt + 1; i < in.Attempt; i++ {
		open, e := isOpen(chain[i])
		if e != nil {
			return e
		}
		if !open {
			return ErrForbidden
		}
	}
	return nil
}
