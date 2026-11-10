package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func (s *server) reportArchives(w http.ResponseWriter, r *http.Request, user store.User, workspace store.Workspace) {
	items, err := s.store.ListReportArchives(r.Context(), workspace.ID, user.ID)
	if err != nil {
		storeFailure(w, err)
		return
	}
	respond(w, http.StatusOK, struct {
		Items           []store.ReportArchive `json:"items"`
		RetentionPerJob int                   `json:"retentionPerJob"`
	}{Items: items, RetentionPerJob: store.ReportArchiveRetentionPerJob})
}

func (s *server) loadReportArchive(w http.ResponseWriter, r *http.Request, user store.User, workspace store.Workspace) (store.ComplianceReport, bool) {
	id, err := strconv.ParseInt(r.PathValue("archive"), 10, 64)
	if err != nil || id < 1 {
		fail(w, http.StatusBadRequest, "报告归档编号无效。")
		return store.ComplianceReport{}, false
	}
	report, err := s.store.ReportArchive(r.Context(), workspace.ID, user.ID, id)
	switch {
	case errors.Is(err, store.ErrReportArchiveFailed):
		fail(w, http.StatusUnprocessableEntity, "这次采集的报告归档生成失败。")
	case err != nil:
		storeFailure(w, err)
	default:
		return report, true
	}
	return store.ComplianceReport{}, false
}

func (s *server) reportArchivePreview(w http.ResponseWriter, r *http.Request, user store.User, workspace store.Workspace) {
	if report, ok := s.loadReportArchive(w, r, user, workspace); ok {
		respond(w, http.StatusOK, report)
	}
}

func (s *server) reportArchiveExport(w http.ResponseWriter, r *http.Request, user store.User, workspace store.Workspace) {
	format, ok := reportExportFormat(w, r)
	if !ok {
		return
	}
	if report, ok := s.loadReportArchive(w, r, user, workspace); ok {
		writeReportExport(w, report, format)
	}
}
