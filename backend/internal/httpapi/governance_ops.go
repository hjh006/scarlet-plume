package httpapi

import (
	"net/http"
	"strconv"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/hjh006/scarlet-plume/backend/pkg/governanceops"
)

func (s *server) governanceOpsRoutes(mux *http.ServeMux) {
	const base = "/api/v1/workspaces/{slug}/governance-ops"
	mux.HandleFunc("GET "+base+"/catalog", s.workspace(false, s.governanceOpsCatalog))
	mux.HandleFunc("GET "+base+"/records", s.workspace(false, s.listGovernanceOps))
	mux.HandleFunc("POST "+base+"/records", s.workspace(false, s.saveGovernanceOps(false)))
	mux.HandleFunc("GET "+base+"/records/{record}", s.workspace(false, s.getGovernanceOps))
	mux.HandleFunc("PUT "+base+"/records/{record}", s.workspace(false, s.saveGovernanceOps(true)))
	mux.HandleFunc("POST "+base+"/records/{record}/actions", s.workspace(false, s.actGovernanceOps))
	mux.HandleFunc("GET "+base+"/records/{record}/history", s.workspace(false, s.governanceOpsHistory))
}

func (s *server) governanceOpsCatalog(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	respond(w, http.StatusOK, map[string]any{"regulations": governanceops.Regulations(), "kinds": governanceops.Kinds, "statuses": governanceops.Statuses, "boundary": governanceops.Boundary})
}
func governanceOpsPagination(w http.ResponseWriter, r *http.Request) (int64, int, bool) {
	cursor := int64(0)
	limit := 50
	var err error
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		cursor, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || cursor < 0 {
			operationsFailure(w, store.ErrInvalid)
			return 0, 0, false
		}
	}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 100 {
			operationsFailure(w, store.ErrInvalid)
			return 0, 0, false
		}
	}
	return cursor, limit, true
}
func (s *server) listGovernanceOps(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	cursor, limit, ok := governanceOpsPagination(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	out, err := s.store.ListGovernanceOps(r.Context(), ws.ID, u.ID, q.Get("kind"), q.Get("status"), cursor, limit)
	if err != nil {
		operationsFailure(w, err)
		return
	}
	respond(w, http.StatusOK, out)
}
func (s *server) saveGovernanceOps(update bool) func(http.ResponseWriter, *http.Request, store.User, store.Workspace) {
	return func(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
		var id int64
		if update {
			var ok bool
			id, ok = operationPathID(w, r, "record")
			if !ok {
				return
			}
		}
		var in governanceops.Input
		if !readJSONLimit(w, r, &in, 64<<10) {
			return
		}
		out, err := s.store.SaveGovernanceOps(r.Context(), ws.ID, u.ID, id, in)
		if err != nil {
			operationsFailure(w, err)
			return
		}
		status := http.StatusCreated
		if update {
			status = http.StatusOK
		}
		respond(w, status, out)
	}
}
func (s *server) getGovernanceOps(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	id, ok := operationPathID(w, r, "record")
	if !ok {
		return
	}
	out, err := s.store.GovernanceOpsRecord(r.Context(), ws.ID, u.ID, id)
	if err != nil {
		operationsFailure(w, err)
		return
	}
	respond(w, http.StatusOK, out)
}
func (s *server) actGovernanceOps(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	id, ok := operationPathID(w, r, "record")
	if !ok {
		return
	}
	var in governanceops.Action
	if !readJSONLimit(w, r, &in, 4<<10) {
		return
	}
	out, err := s.store.ActGovernanceOps(r.Context(), ws.ID, u.ID, id, in)
	if err != nil {
		operationsFailure(w, err)
		return
	}
	respond(w, http.StatusOK, out)
}
func (s *server) governanceOpsHistory(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	id, ok := operationPathID(w, r, "record")
	if !ok {
		return
	}
	cursor, limit, ok := governanceOpsPagination(w, r)
	if !ok {
		return
	}
	out, err := s.store.GovernanceOpsHistory(r.Context(), ws.ID, u.ID, id, cursor, limit)
	if err != nil {
		operationsFailure(w, err)
		return
	}
	respond(w, http.StatusOK, out)
}
