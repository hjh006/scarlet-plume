package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/runtimesecurity"
)

type runtimeControlCommand struct {
	ID       int64  `json:"id"`
	Revision int64  `json:"revision"`
	Phase    string `json:"phase"`
	Status   string `json:"status"`
	Spec     struct {
		Method string `json:"method"`
		Route  string `json:"route"`
	} `json:"spec"`
	ExpiresAt time.Time `json:"expiresAt"`
}
type runtimeControls struct {
	endpoint, token string
	client          *http.Client
	templates       []routeTemplate
	telemetry       *observer
	mu              sync.RWMutex
	blocks          map[string]runtimeControlCommand
	receipts        map[string][]byte
	confirmed       map[string]bool
}

func newRuntimeControls(origin, token string, local bool, templates []routeTemplate, telemetry *observer) (*runtimeControls, error) {
	u, err := receiverEndpoint(origin, local)
	if err != nil {
		return nil, err
	}
	parsed, err := parseOrigin(origin)
	if err != nil {
		return nil, err
	}
	_ = u // The receiver validator enforces the same TLS and local-origin boundary.
	parsed.Path = "/api/v1/runtime/controls"
	return &runtimeControls{endpoint: parsed.String(), token: token, client: &http.Client{Transport: directTransport(), Timeout: 1500 * time.Millisecond, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, templates: templates, telemetry: telemetry, blocks: map[string]runtimeControlCommand{}, receipts: map[string][]byte{}, confirmed: map[string]bool{}}, nil
}

func (c *runtimeControls) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route := matchRoute(c.templates, r.URL.Path)
		key := r.Method + " " + runtimesecurity.RouteKey(route)
		c.mu.RLock()
		command, blocked := c.blocks[key]
		c.mu.RUnlock()
		if !blocked || !time.Now().Before(command.ExpiresAt) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Scarlet-Control", strconv.FormatInt(command.ID, 10))
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":"access restricted by an approved policy"}`)
		// This fixed response was inspected in its entirety. It contains no
		// upstream data; authentication identity remains unknown at this boundary.
		c.telemetry.enqueue(observationEvent{Method: r.Method, Route: route, Status: 403, ObservedAt: time.Now().UTC(), InspectedLocations: []string{"response"}, SecuritySignals: runtimesecurity.InspectSecurity([]byte(r.URL.RawQuery))})
	})
}

func (c *runtimeControls) run(ctx context.Context) {
	defer c.client.CloseIdleConnections()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		_ = c.refresh(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (c *runtimeControls) refresh(ctx context.Context) error {
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint, nil)
	if err != nil {
		return err
	}
	r.Header.Set("Authorization", "Bearer "+c.token)
	response, err := c.client.Do(r)
	if err != nil {
		return errors.New("control source unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return errors.New("control source rejected")
	}
	var state struct {
		Items []runtimeControlCommand `json:"items"`
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 512<<10+1))
	if err != nil || len(raw) > 512<<10 {
		return errors.New("control scope exceeds limit")
	}
	if json.Unmarshal(raw, &state) != nil || len(state.Items) > 200 {
		return errors.New("invalid control state")
	}
	next := map[string]runtimeControlCommand{}
	for _, cmd := range state.Items {
		if cmd.ID < 1 || cmd.Revision < 1 || !strings.HasPrefix(cmd.Spec.Route, "/") || (cmd.Phase != "apply" && cmd.Phase != "rollback") {
			return errors.New("invalid control command")
		}
		if _, err := runtimesecurity.NormalizeObservation(runtimesecurity.Observation{Method: cmd.Spec.Method, Route: cmd.Spec.Route, Status: 403, ObservedAt: time.Now()}); err != nil {
			return errors.New("invalid control route")
		}
		trusted := false
		for _, route := range c.templates {
			if runtimesecurity.RouteKey(route.pattern) == runtimesecurity.RouteKey(cmd.Spec.Route) {
				trusted = true
				break
			}
		}
		if !trusted {
			return errors.New("control route is outside configured templates")
		}
		if cmd.Phase == "apply" && time.Now().Before(cmd.ExpiresAt) {
			next[cmd.Spec.Method+" "+runtimesecurity.RouteKey(cmd.Spec.Route)] = cmd
		}
	}
	c.mu.Lock()
	c.blocks = next
	c.mu.Unlock()
	for _, cmd := range state.Items {
		if cmd.Status != "approved" && cmd.Status != "rollback_requested" && cmd.Phase != "rollback" {
			continue
		}
		key := strconv.FormatInt(cmd.ID, 10) + "/" + strconv.FormatInt(cmd.Revision, 10) + "/" + cmd.Phase
		if c.confirmed[key] {
			continue
		}
		payload := c.receipts[key]
		if payload == nil {
			code := "route_blocked"
			if cmd.Phase == "rollback" {
				code = "route_released"
			}
			payload, _ = json.Marshal(map[string]any{"actionId": cmd.ID, "revision": cmd.Revision, "result": map[string]any{"phase": cmd.Phase, "success": true, "code": code, "observedAt": time.Now().UTC()}})
			c.receipts[key] = payload
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/receipts", bytes.NewReader(payload))
		if err != nil {
			return err
		}
		request.Header.Set("Authorization", "Bearer "+c.token)
		request.Header.Set("Content-Type", "application/json")
		receipt, err := c.client.Do(request)
		if err != nil {
			return errors.New("control receipt unavailable")
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(receipt.Body, 4096))
		_ = receipt.Body.Close()
		if receipt.StatusCode == 200 {
			c.confirmed[key] = true
			delete(c.receipts, key)
		}
	}
	return nil
}
