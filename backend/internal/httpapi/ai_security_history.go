package httpapi

import (
	"net/http"
	"strconv"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
)

func (s *server) aiSecurityHistoryRoutes(mux *http.ServeMux) {
	const base = "/api/v1/workspaces/{slug}/ai-security/"
	for _, collection := range []string{"decisions", "approvals", "evaluations"} {
		mux.HandleFunc("GET "+base+collection, s.workspace(false, func(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
			s.aiSecurityHistoryList(w, r, u, ws, collection, false)
		}))
		mux.HandleFunc("GET "+base+collection+"/{record}", s.workspace(false, func(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
			s.aiSecurityHistoryItem(w, r, u, ws, collection)
		}))
	}
	for _, collection := range []string{"decisions", "approvals", "evaluations", "regression-packs"} {
		mux.HandleFunc("GET "+base+collection+"/export", s.workspace(false, func(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
			s.aiSecurityHistoryList(w, r, u, ws, collection, true)
		}))
	}
}

func aiHistoryQuery(r *http.Request) (q store.AISecurityHistoryQuery, err error) {
	for name, values := range r.URL.Query() {
		if len(values) != 1 || name != "before" && name != "limit" && name != "kind" && name != "outcome" && name != "status" {
			return q, store.ErrInvalid
		}
	}
	q.Limit, err = gatewayTaskLimit(r)
	q.Before, q.Kind, q.Outcome, q.Status = r.URL.Query().Get("before"), r.URL.Query().Get("kind"), r.URL.Query().Get("outcome"), r.URL.Query().Get("status")
	return q, err
}

func aiHistoryResponse[T any](w http.ResponseWriter, r *http.Request, ws store.Workspace, collection string, q store.AISecurityHistoryQuery, page store.AISecurityHistoryPage[T], exporting bool) {
	out := map[string]any{"items": page.Items, "limit": page.Limit, "hasMore": page.HasMore, "nextCursor": page.NextCursor}
	if collection == "regression-packs" {
		out["requiredDimensions"], out["latestGate"], out["engineVersion"], out["inspectionVersion"] = store.AISecurityRegressionDimensions(), store.AISecurityRegressionGate, aisecurity.RuntimeProtocolVersion, aisecurity.InspectionVersion
	}
	if exporting {
		filters := map[string]string{}
		if q.Kind != "" {
			filters["kind"] = q.Kind
		}
		if q.Outcome != "" {
			filters["outcome"] = q.Outcome
		}
		if q.Status != "" {
			filters["status"] = q.Status
		}
		out["kind"], out["mode"], out["workspace"], out["collection"] = "ai-security-history-page", "control_plane_history", ws.Slug, collection
		out["scope"] = map[string]any{"mode": "filtered_page", "filters": filters, "limit": page.Limit, "before": q.Before, "hasMore": page.HasMore, "nextCursor": page.NextCursor, "readAt": time.Now().UTC(), "snapshot": "re_read_at_export_time", "limitations": []string{"仅导出本次过滤和分页重新读取的历史记录，不表示全量历史或此前页面快照。", "历史配置、引擎、检测与门槛版本仅按记录展示；缺失版本不以当前版本补算。", "评估、审批与发布资格仍需现行目录修订及服务端授权校验，历史结果不代表实际执行。"}}
		w.Header().Set("Content-Disposition", `attachment; filename="ai-security-`+collection+`-page.json"`)
	}
	respond(w, 200, out)
}

func (s *server) aiSecurityHistoryList(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace, collection string, exporting bool) {
	q, err := aiHistoryQuery(r)
	if err != nil {
		operationsFailure(w, err)
		return
	}
	switch collection {
	case "decisions":
		out, err := s.store.AISecurityDecisions(r.Context(), ws.ID, u.ID, q)
		if err != nil {
			operationsFailure(w, err)
			return
		}
		aiHistoryResponse(w, r, ws, collection, q, out, exporting)
	case "approvals":
		out, err := s.store.AISecurityApprovalsPage(r.Context(), ws.ID, u.ID, q)
		if err != nil {
			operationsFailure(w, err)
			return
		}
		aiHistoryResponse(w, r, ws, collection, q, out, exporting)
	case "evaluations":
		out, err := s.store.AISecurityEvaluationsPage(r.Context(), ws.ID, u.ID, q)
		if err != nil {
			operationsFailure(w, err)
			return
		}
		aiHistoryResponse(w, r, ws, collection, q, out, exporting)
	case "regression-packs":
		out, err := s.store.AISecurityRegressionPackPage(r.Context(), ws.ID, u.ID, q)
		if err != nil {
			operationsFailure(w, err)
			return
		}
		aiHistoryResponse(w, r, ws, collection, q, out, exporting)
	}
}

func (s *server) aiSecurityHistoryItem(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace, collection string) {
	id, err := strconv.ParseInt(r.PathValue("record"), 10, 64)
	if err != nil || id < 1 {
		operationsFailure(w, store.ErrInvalid)
		return
	}
	var out any
	switch collection {
	case "decisions":
		out, err = s.store.AISecurityDecision(r.Context(), ws.ID, u.ID, id)
	case "approvals":
		out, err = s.store.AISecurityApproval(r.Context(), ws.ID, u.ID, id)
	case "evaluations":
		out, err = s.store.AISecurityEvaluation(r.Context(), ws.ID, u.ID, id)
	}
	if err != nil {
		operationsFailure(w, err)
		return
	}
	respond(w, 200, out)
}
