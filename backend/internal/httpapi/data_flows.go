package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func (s *server) dataFlowRoutes(mux *http.ServeMux) {
	const base = "/api/v1/workspaces/{slug}/data-flows"
	mux.HandleFunc("GET "+base, s.workspace(false, s.dataFlowGraph(false)))
	mux.HandleFunc("GET "+base+"/export", s.workspace(false, s.dataFlowGraph(true)))
	mux.HandleFunc("GET "+base+"/snapshots", s.workspace(false, s.dataFlowSnapshots))
	mux.HandleFunc("POST "+base+"/snapshots", s.workspace(true, s.captureDataFlowSnapshot))
	mux.HandleFunc("GET "+base+"/snapshots/{snapshot}", s.workspace(false, s.dataFlowSnapshot))
	mux.HandleFunc("GET "+base+"/compare", s.workspace(false, s.compareDataFlows))
	mux.HandleFunc("GET "+base+"/investigation", s.workspace(false, s.investigateDataFlows))
}

func dataFlowQuery(r *http.Request) store.DataFlowFilter {
	q := r.URL.Query()
	return store.DataFlowFilter{Service: strings.TrimSpace(q.Get("service")), Category: strings.TrimSpace(q.Get("category")), SubjectRef: strings.TrimSpace(q.Get("subjectRef")), TraceRef: strings.TrimSpace(q.Get("traceRef")), Focus: strings.TrimSpace(q.Get("focus"))}
}

func dataFlowFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrInvalid):
		fail(w, http.StatusBadRequest, "请核对服务、分类、引用格式及版本范围；比较的快照须采用相同筛选条件。")
	case errors.Is(err, store.ErrForbidden):
		fail(w, http.StatusForbidden, "当前账号无权保存数据流快照。")
	case errors.Is(err, store.ErrDataFlowLimit):
		fail(w, http.StatusUnprocessableEntity, "图谱超出本次支持的观察范围，请按服务、分类或引用筛选后重试。")
	default:
		storeFailure(w, err)
	}
}

func (s *server) dataFlowGraph(export bool) workspaceAction {
	return func(w http.ResponseWriter, r *http.Request, user store.User, workspace store.Workspace) {
		graph, err := s.store.DataFlows(r.Context(), workspace.ID, user.ID, dataFlowQuery(r))
		if err != nil {
			dataFlowFailure(w, err)
			return
		}
		if export {
			w.Header().Set("Content-Disposition", `attachment; filename="scarlet-data-flows.json"`)
		}
		respond(w, http.StatusOK, graph)
	}
}

func (s *server) dataFlowSnapshots(w http.ResponseWriter, r *http.Request, user store.User, workspace store.Workspace) {
	items, err := s.store.ListDataFlowSnapshots(r.Context(), workspace.ID, user.ID)
	if err != nil {
		dataFlowFailure(w, err)
		return
	}
	respond(w, http.StatusOK, map[string]any{"items": items, "limit": 30})
}

func (s *server) captureDataFlowSnapshot(w http.ResponseWriter, r *http.Request, user store.User, workspace store.Workspace) {
	var filter store.DataFlowFilter
	if !readJSONLimit(w, r, &filter, 4096) {
		return
	}
	filter.Service = strings.TrimSpace(filter.Service)
	filter.Category = strings.TrimSpace(filter.Category)
	filter.SubjectRef = strings.TrimSpace(filter.SubjectRef)
	filter.TraceRef = strings.TrimSpace(filter.TraceRef)
	filter.Focus = strings.TrimSpace(filter.Focus)
	snapshot, err := s.store.CaptureDataFlowSnapshot(r.Context(), workspace.ID, user.ID, filter)
	if err != nil {
		dataFlowFailure(w, err)
		return
	}
	// Saving unchanged structure may reuse a retained snapshot.
	respond(w, http.StatusOK, snapshot)
}

func (s *server) dataFlowSnapshot(w http.ResponseWriter, r *http.Request, user store.User, workspace store.Workspace) {
	id, err := strconv.ParseInt(r.PathValue("snapshot"), 10, 64)
	if err != nil || id < 1 {
		fail(w, http.StatusBadRequest, "请选择有效的数据流快照。")
		return
	}
	snapshot, err := s.store.DataFlowSnapshot(r.Context(), workspace.ID, user.ID, id)
	if err != nil {
		dataFlowFailure(w, err)
		return
	}
	respond(w, http.StatusOK, snapshot)
}

func (s *server) compareDataFlows(w http.ResponseWriter, r *http.Request, user store.User, workspace store.Workspace) {
	from, ok := optionalQueryID(w, r, "from")
	if !ok {
		return
	}
	to, ok := optionalQueryID(w, r, "to")
	if !ok {
		return
	}
	if from == 0 || to == 0 {
		fail(w, http.StatusBadRequest, "请选择两个数据流快照进行比较。")
		return
	}
	comparison, err := s.store.CompareDataFlowSnapshots(r.Context(), workspace.ID, user.ID, from, to)
	if err != nil {
		dataFlowFailure(w, err)
		return
	}
	respond(w, http.StatusOK, comparison)
}

func (s *server) investigateDataFlows(w http.ResponseWriter, r *http.Request, user store.User, workspace store.Workspace) {
	result, err := s.store.InvestigateDataFlows(r.Context(), workspace.ID, user.ID, dataFlowQuery(r))
	if err != nil {
		dataFlowFailure(w, err)
		return
	}
	respond(w, http.StatusOK, result)
}
