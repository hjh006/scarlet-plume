package httpapi

import (
	"encoding/json"
	"html/template"
	"net/http"
	"strconv"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

const endpointHeartbeatPath = "/api/v1/endpoint/heartbeat"
const endpointPolicyPath = "/api/v1/endpoint/policy"
const endpointSessionEventsPath = "/api/v1/endpoint/session-events"
const endpointReceiptsPath = "/api/v1/endpoint/receipts"

func (s *server) endpointChannelRoutes(mux *http.ServeMux) {
	const base = "/api/v1/workspaces/{slug}/endpoint"
	mux.HandleFunc("GET "+base+"/devices", s.workspace(false, s.endpointDevices))
	mux.HandleFunc("GET "+base+"/policies", s.workspace(false, s.endpointPolicies))
	mux.HandleFunc("POST "+base+"/policies", s.workspace(false, s.createEndpointPolicy))
	mux.HandleFunc("GET "+base+"/policies/{policy}", s.workspace(false, s.endpointPolicyDetail))
	mux.HandleFunc("POST "+base+"/policies/{policy}/{decision}", s.workspace(false, s.decideEndpointPolicy))
	mux.HandleFunc("GET "+base+"/exports/{export}/export", s.workspace(false, s.exportEndpointEvidence))
	mux.HandleFunc("POST "+endpointHeartbeatPath, s.operationsExternal("security_events", s.endpointHeartbeat))
	mux.HandleFunc("GET "+endpointPolicyPath, s.operationsExternal("security_events", s.endpointAgentPolicy))
	mux.HandleFunc("POST "+endpointSessionEventsPath, s.operationsExternal("security_events", s.endpointSessionEvent))
	mux.HandleFunc("POST "+endpointReceiptsPath, s.operationsExternal("security_events", s.endpointReceipt))
}
func (s *server) endpointDevices(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	items, err := s.store.ListEndpointDevices(r.Context(), ws.ID, u.ID)
	if err != nil {
		operationsFailure(w, err)
		return
	}
	respond(w, 200, map[string]any{"items": items, "limit": 100})
}
func (s *server) endpointPolicies(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	source := int64(0)
	if v := r.URL.Query().Get("sourceId"); v != "" {
		var err error
		source, err = strconv.ParseInt(v, 10, 64)
		if err != nil || source < 1 {
			operationsFailure(w, store.ErrInvalid)
			return
		}
	}
	items, err := s.store.ListEndpointPolicies(r.Context(), ws.ID, u.ID, source, r.URL.Query().Get("channel"))
	if err != nil {
		operationsFailure(w, err)
		return
	}
	respond(w, 200, map[string]any{"items": items, "limit": 100})
}
func (s *server) createEndpointPolicy(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	var in store.EndpointPolicyInput
	if !readJSONLimit(w, r, &in, operationsBodyLimit) {
		return
	}
	result, err := s.store.CreateEndpointPolicy(r.Context(), ws.ID, u.ID, in)
	if err != nil {
		operationsFailure(w, err)
		return
	}
	respond(w, 201, result)
}
func (s *server) endpointPolicyDetail(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	id, ok := operationPathID(w, r, "policy")
	if !ok {
		return
	}
	result, err := s.store.EndpointPolicyDetail(r.Context(), ws.ID, u.ID, id)
	if err != nil {
		operationsFailure(w, err)
		return
	}
	respond(w, 200, result)
}
func (s *server) decideEndpointPolicy(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	id, ok := operationPathID(w, r, "policy")
	if !ok {
		return
	}
	if r.PathValue("decision") == "exports" {
		var empty struct{}
		if !readJSONLimit(w, r, &empty, 1024) {
			return
		}
		result, err := s.store.CreateEndpointEvidenceExport(r.Context(), ws.ID, u.ID, id)
		if err != nil {
			operationsFailure(w, err)
			return
		}
		respond(w, 201, result)
		return
	}
	var in struct {
		Revision int64  `json:"revision"`
		Note     string `json:"note"`
		Reason   string `json:"reason"`
	}
	if !readJSONLimit(w, r, &in, 2048) {
		return
	}
	note := in.Note
	if r.PathValue("decision") == "revoke" {
		note = in.Reason
		if in.Note != "" {
			operationsFailure(w, store.ErrInvalid)
			return
		}
	} else if in.Reason != "" {
		operationsFailure(w, store.ErrInvalid)
		return
	}
	result, err := s.store.DecideEndpointPolicy(r.Context(), ws.ID, u.ID, id, in.Revision, r.PathValue("decision"), note)
	if err != nil {
		operationsFailure(w, err)
		return
	}
	respond(w, 200, result)
}
func (s *server) endpointHeartbeat(w http.ResponseWriter, r *http.Request, digest string) {
	var in store.EndpointHeartbeatInput
	if !readJSONLimit(w, r, &in, operationsBodyLimit) {
		return
	}
	result, err := s.store.EndpointHeartbeat(r.Context(), digest, in)
	if err != nil {
		operationsFailure(w, err)
		return
	}
	respond(w, 200, result)
}
func (s *server) endpointAgentPolicy(w http.ResponseWriter, r *http.Request, digest string) {
	if len(r.URL.Query()["deviceRef"]) != 1 {
		operationsFailure(w, store.ErrInvalid)
		return
	}
	result, err := s.store.EndpointPoliciesForAgent(r.Context(), digest, r.URL.Query().Get("deviceRef"))
	if err != nil {
		operationsFailure(w, err)
		return
	}
	respond(w, 200, result)
}
func (s *server) endpointReceipt(w http.ResponseWriter, r *http.Request, digest string) {
	var in store.EndpointReceiptInput
	if !readJSONLimit(w, r, &in, operationsBodyLimit) {
		return
	}
	result, replay, err := s.store.ReceiveEndpointReceipt(r.Context(), digest, in)
	if err != nil {
		operationsFailure(w, err)
		return
	}
	status := 201
	if replay {
		status = 200
	}
	respond(w, status, map[string]any{"receipt": result, "replayed": replay})
}

var endpointEvidenceHTML = template.Must(template.New("endpoint").Parse(`<!doctype html><html lang="zh-CN"><meta charset="utf-8"><title>终端通道证据</title><style>body{font:15px sans-serif;max-width:1080px;margin:40px auto;line-height:1.6}pre{white-space:pre-wrap;overflow-wrap:anywhere}h1{font-size:25px}</style><h1>终端通道证据快照 #{{.ID}}</h1><p>生成：{{.CreatedAt}} · {{.Actor}}</p><p>SHA-256：{{.SnapshotDigest}}</p>{{range .Snapshot.Boundaries}}<p>{{.}}</p>{{end}}<h2>冻结策略、设备、回执及操作清单</h2><pre>{{.JSON}}</pre></html>`))

func (s *server) exportEndpointEvidence(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	id, ok := operationPathID(w, r, "export")
	if !ok {
		return
	}
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "json"
	}
	if format != "json" && format != "html" {
		operationsFailure(w, store.ErrInvalid)
		return
	}
	result, err := s.store.EndpointEvidenceExport(r.Context(), ws.ID, u.ID, id)
	if err != nil {
		operationsFailure(w, err)
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="endpoint-evidence-`+strconv.FormatInt(id, 10)+`.`+format+`"`)
	if format == "json" {
		respond(w, 200, result)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	raw, _ := json.MarshalIndent(result.Snapshot, "", "  ")
	_ = endpointEvidenceHTML.Execute(w, struct {
		store.EndpointEvidenceExport
		JSON string
	}{result, string(raw)})
}

func (s *server) endpointSessionEvent(w http.ResponseWriter, r *http.Request, digest string) {
	var in store.EndpointSessionEventInput
	if !readJSONLimit(w, r, &in, 4096) {
		return
	}
	event, replay, err := s.store.ReceiveEndpointSessionEvent(r.Context(), digest, in)
	if err != nil {
		operationsFailure(w, err)
		return
	}
	status := http.StatusCreated
	if replay {
		status = http.StatusOK
	}
	respond(w, status, map[string]any{"event": event, "replayed": replay})
}
