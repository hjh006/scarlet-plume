package httpapi

import (
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode"

	"github.com/hjh006/scarlet-plume/backend/internal/collector"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func (s *server) classificationWorkbenchRoutes(mux *http.ServeMux) {
	s.classificationAIRoutes(mux)
	s.classificationIntelligenceRoutes(mux)
	const base = "/api/v1/workspaces/{slug}/classification"
	mux.HandleFunc("GET "+base+"/workbench", s.workspace(false, s.classificationWorkbench))
	mux.HandleFunc("POST "+base+"/assets/batch-review", s.workspace(false, s.classificationBatchReview))
	mux.HandleFunc("GET "+base+"/taxonomy", s.workspace(false, s.classificationTaxonomy))
	mux.HandleFunc("POST "+base+"/taxonomy", s.workspace(false, s.saveClassificationTaxonomy))
	mux.HandleFunc("PUT "+base+"/taxonomy/{id}", s.workspace(false, s.saveClassificationTaxonomy))
	mux.HandleFunc("GET "+base+"/publications", s.workspace(false, s.classificationPublications))
	mux.HandleFunc("POST "+base+"/publications", s.workspace(false, s.createClassificationPublication))
	mux.HandleFunc("GET "+base+"/publications/{id}", s.workspace(false, s.classificationPublication))
	mux.HandleFunc("POST "+base+"/publications/{id}/review", s.workspace(false, s.reviewClassificationPublication))
	mux.HandleFunc("GET "+base+"/publications/{id}/export.csv", s.workspace(false, s.exportClassificationPublicationCSV))
	mux.HandleFunc("GET "+base+"/publications/{id}/export.json", s.workspace(false, s.exportClassificationPublicationJSON))
	mux.HandleFunc("POST /api/v1/workspaces/{slug}/discovery/rules/test", s.workspace(false, s.testClassificationRule))
}

func classificationWorkbenchFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrForbidden):
		fail(w, 403, "当前身份不能执行此操作；发布评审人须独立于提交人与分类经办人。")
	case errors.Is(err, store.ErrConflict):
		fail(w, 409, "记录、目录或来源状态已变化，请刷新后重新核对。")
	case errors.Is(err, store.ErrInvalid):
		fail(w, 400, "请核对选择范围、分类依据和表单字段；发布需要完整的人工确认分类及标准依据。")
	default:
		classificationFailure(w, err)
	}
}

func (s *server) classificationWorkbench(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	page, size := 1, 50
	for key, target := range map[string]*int{"page": &page, "pageSize": &size} {
		if value := r.URL.Query().Get(key); value != "" {
			n, e := strconv.Atoi(value)
			if e != nil || n < 1 {
				fail(w, 400, "分页参数无效。")
				return
			}
			*target = n
		}
	}
	taxonomy, ok := optionalQueryID(w, r, "taxonomyId")
	if !ok {
		return
	}
	q := r.URL.Query()
	result, err := s.store.ClassificationWorkbench(r.Context(), ws.ID, u.ID, store.ClassificationWorkbenchFilter{Page: page, PageSize: size, Q: q.Get("q"), Source: q.Get("source"), System: q.Get("system"), Kind: q.Get("kind"), Category: q.Get("category"), BusinessLevel: q.Get("businessLevel"), StatutoryCategory: q.Get("statutoryCategory"), Status: q.Get("status"), TaxonomyID: taxonomy})
	if err != nil {
		classificationWorkbenchFailure(w, err)
		return
	}
	respond(w, 200, result)
}
func (s *server) classificationBatchReview(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	var input store.ClassificationBatchReviewInput
	if !readJSONLimit(w, r, &input, 64<<10) {
		return
	}
	result, err := s.store.BatchReviewAssetClassifications(r.Context(), ws.ID, u.ID, input)
	if err != nil {
		classificationWorkbenchFailure(w, err)
		return
	}
	respond(w, 200, map[string]any{"items": result})
}
func (s *server) classificationTaxonomy(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	result, err := s.store.ListClassificationTaxonomy(r.Context(), ws.ID, u.ID)
	if err != nil {
		classificationWorkbenchFailure(w, err)
		return
	}
	respond(w, 200, map[string]any{"items": result})
}
func (s *server) saveClassificationTaxonomy(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	var input store.ClassificationTaxonomyInput
	if !readJSONLimit(w, r, &input, 8<<10) {
		return
	}
	var id int64
	if r.Method == http.MethodPut {
		var ok bool
		id, ok = discoveryID(w, r)
		if !ok {
			return
		}
	}
	result, err := s.store.SaveClassificationTaxonomy(r.Context(), ws.ID, u.ID, id, input)
	if err != nil {
		classificationWorkbenchFailure(w, err)
		return
	}
	status := 200
	if r.Method == http.MethodPost {
		status = 201
	}
	respond(w, status, result)
}
func (s *server) classificationPublications(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	result, err := s.store.ListClassificationPublications(r.Context(), ws.ID, u.ID)
	if err != nil {
		classificationWorkbenchFailure(w, err)
		return
	}
	respond(w, 200, map[string]any{"items": result, "limit": 50})
}
func (s *server) createClassificationPublication(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	var input store.ClassificationPublicationInput
	if !readJSONLimit(w, r, &input, 64<<10) {
		return
	}
	result, err := s.store.CreateClassificationPublication(r.Context(), ws.ID, u.ID, input)
	if err != nil {
		classificationWorkbenchFailure(w, err)
		return
	}
	respond(w, 201, result)
}
func (s *server) classificationPublication(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	id, ok := discoveryID(w, r)
	if !ok {
		return
	}
	result, err := s.store.GetClassificationPublication(r.Context(), ws.ID, u.ID, id)
	if err != nil {
		classificationWorkbenchFailure(w, err)
		return
	}
	respond(w, 200, result)
}
func (s *server) reviewClassificationPublication(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	id, ok := discoveryID(w, r)
	if !ok {
		return
	}
	var input store.ClassificationPublicationReviewInput
	if !readJSONLimit(w, r, &input, 8<<10) {
		return
	}
	result, err := s.store.ReviewClassificationPublication(r.Context(), ws.ID, u.ID, id, input)
	if err != nil {
		classificationWorkbenchFailure(w, err)
		return
	}
	respond(w, 200, result)
}

func (s *server) publishedClassification(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) (store.ClassificationPublication, bool) {
	id, ok := discoveryID(w, r)
	if !ok {
		return store.ClassificationPublication{}, false
	}
	result, err := s.store.GetClassificationPublication(r.Context(), ws.ID, u.ID, id)
	if err != nil {
		classificationWorkbenchFailure(w, err)
		return result, false
	}
	if result.Status != "published" {
		fail(w, 409, "只有经独立评审发布的分类快照可用于导出与同步。")
		return result, false
	}
	return result, true
}
func (s *server) exportClassificationPublicationJSON(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	result, ok := s.publishedClassification(w, r, u, ws)
	if !ok {
		return
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="classification-%d.json"`, result.ID))
	respond(w, 200, result)
}

// Prefix spreadsheet formulas even when preceded by whitespace. CSV escaping
// handles delimiters; this also keeps metadata from becoming executable cells.
func classificationCSVCell(value string) string {
	start := strings.TrimLeftFunc(value, unicode.IsSpace)
	if start != "" && strings.ContainsRune("=+-@", rune(start[0])) {
		return "'" + value
	}
	return value
}
func (s *server) exportClassificationPublicationCSV(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	result, ok := s.publishedClassification(w, r, u, ws)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="classification-%d.csv"`, result.ID))
	if _, err := w.Write([]byte("\ufeff")); err != nil {
		return
	}
	writer := csv.NewWriter(w)
	_ = writer.Write([]string{"资产ID", "资产名称", "来源", "系统", "类型", "数据库", "Schema", "表/集合", "字段/路径", "业务分类", "数据属性", "内部等级", "法定类别", "采用标准", "标准等级", "采用条款", "分类修订", "判定依据", "发布ID", "快照摘要"})
	for _, item := range result.Items {
		kind, database, schema, table, field := "asset", "", "", "", ""
		if item.Catalog != nil {
			kind, database, schema, table, field = item.Catalog.Kind, item.Catalog.Database, item.Catalog.Schema, item.Catalog.Table, item.Catalog.Field
		}
		taxonomy := ""
		if item.Taxonomy != nil {
			taxonomy = item.Taxonomy.Path
		}
		standard, level, clause := "", "", ""
		if item.Standard != nil {
			standard, level, clause = item.Standard.Code, item.Standard.LevelLabel, item.Standard.RuleClause
		}
		row := []string{strconv.FormatInt(item.Asset.ID, 10), item.Asset.Name, item.Asset.Source, item.Asset.System, kind, database, schema, table, field, taxonomy, item.Asset.Category, item.Asset.BusinessLevel, item.Asset.StatutoryCategory, standard, level, clause, strconv.FormatInt(item.Revision, 10), item.Asset.ClassificationBasis, strconv.FormatInt(result.ID, 10), result.SnapshotDigest}
		for i := range row {
			row[i] = classificationCSVCell(row[i])
		}
		if writer.Write(row) != nil {
			return
		}
	}
	writer.Flush()
}

func (s *server) testClassificationRule(w http.ResponseWriter, r *http.Request, _ store.User, ws store.Workspace) {
	if ws.Role != "owner" && ws.Role != "operator" {
		fail(w, 403, "当前身份仅可查看规则；规则校准由负责人或经办人执行。")
		return
	}
	var input collector.RuleCalibrationInput
	if !readJSONLimit(w, r, &input, 64<<10) {
		return
	}
	result, err := collector.CalibrateDiscoveryRule(input)
	if err != nil {
		fail(w, 400, "规则或合成样本无效，请核对类型、表达式、样本数量及长度。")
		return
	}
	respond(w, 200, result)
}
