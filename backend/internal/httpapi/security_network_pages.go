package httpapi

import (
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func (s *server) securityNetworkPageRoutes(mux *http.ServeMux) {
	const base = "/api/v1/workspaces/{slug}/security-platform"
	mux.HandleFunc("GET "+base+"/network-bindings", s.workspace(false, s.getSecurityNetworkBindings))
	mux.HandleFunc("GET "+base+"/network-bindings/{binding}", s.workspace(false, s.getSecurityNetworkBinding))
	mux.HandleFunc("GET "+base+"/network-changes", s.workspace(false, s.getSecurityNetworkChanges))
	mux.HandleFunc("GET "+base+"/snapshots", s.workspace(false, s.getSecurityPlatformSnapshots))
}

func securityNetworkPageQuery(r *http.Request, filterNames ...string) (url.Values, int, error) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, 0, store.ErrInvalid
	}
	allowed := map[string]bool{"cursor": true, "limit": true}
	for _, name := range filterNames {
		allowed[name] = true
	}
	for name, values := range q {
		if !allowed[name] || len(values) != 1 {
			return nil, 0, store.ErrInvalid
		}
	}
	limit := 50
	if values, ok := q["limit"]; ok {
		limit, err = strconv.Atoi(values[0])
		if err != nil || limit < 1 || limit > 200 {
			return nil, 0, store.ErrInvalid
		}
	}
	return q, limit, nil
}

func (s *server) getSecurityNetworkBindings(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	q, limit, err := securityNetworkPageQuery(r, "q")
	if err != nil {
		securityPlatformFailure(w, err)
		return
	}
	out, err := s.store.SecurityNetworkBindings(r.Context(), ws.ID, u.ID, q.Get("q"), q.Get("cursor"), limit)
	if err != nil {
		securityPlatformFailure(w, err)
		return
	}
	respond(w, http.StatusOK, out)
}

func (s *server) getSecurityNetworkBinding(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	id, err := strconv.ParseInt(r.PathValue("binding"), 10, 64)
	if err != nil || id < 1 {
		securityPlatformFailure(w, store.ErrInvalid)
		return
	}
	out, err := s.store.SecurityNetworkBinding(r.Context(), ws.ID, u.ID, id)
	if err != nil {
		securityPlatformFailure(w, err)
		return
	}
	respond(w, http.StatusOK, out)
}

func (s *server) getSecurityNetworkChanges(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	q, limit, err := securityNetworkPageQuery(r, "bindingId", "status")
	if err != nil {
		securityPlatformFailure(w, err)
		return
	}
	filter := store.SecurityNetworkChangeFilter{Status: q.Get("status")}
	if values, present := q["bindingId"]; present {
		filter.BindingID, err = strconv.ParseInt(values[0], 10, 64)
		if err != nil || filter.BindingID < 1 {
			securityPlatformFailure(w, store.ErrInvalid)
			return
		}
	}
	out, err := s.store.SecurityNetworkChanges(r.Context(), ws.ID, u.ID, filter, q.Get("cursor"), limit)
	if err != nil {
		securityPlatformFailure(w, err)
		return
	}
	respond(w, http.StatusOK, out)
}

func (s *server) getSecurityPlatformSnapshots(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	q, limit, err := securityNetworkPageQuery(r, "createdFrom", "createdTo")
	if err != nil {
		securityPlatformFailure(w, err)
		return
	}
	filter := store.SecurityPlatformSnapshotFilter{}
	for _, field := range []struct {
		name   string
		target **time.Time
	}{{"createdFrom", &filter.CreatedFrom}, {"createdTo", &filter.CreatedTo}} {
		if values, present := q[field.name]; present {
			value, err := time.Parse(time.RFC3339Nano, values[0])
			if err != nil {
				securityPlatformFailure(w, store.ErrInvalid)
				return
			}
			*field.target = &value
		}
	}
	out, err := s.store.SecurityPlatformSnapshots(r.Context(), ws.ID, u.ID, filter, q.Get("cursor"), limit)
	if err != nil {
		securityPlatformFailure(w, err)
		return
	}
	respond(w, http.StatusOK, out)
}
