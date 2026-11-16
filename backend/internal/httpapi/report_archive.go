package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func (s *server) reportArchives(w http.ResponseWriter, r *http.Request, user store.User, workspace store.Workspace) {
	cursor, limit, ok := governanceOpsPagination(w, r)
	if !ok {
		return
	}
	page, err := s.store.ListReportArchivePage(r.Context(), workspace.ID, user.ID, cursor, limit)
	if err != nil {
		reportArchiveFailure(w, err)
		return
	}
	respond(w, http.StatusOK, page)
}

func reportArchiveFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrReportArchiveIntegrity):
		fail(w, 422, "归档内容或摘要完整性核验失败，已停止预览或导出。请保留记录并核查数据库和备份。")
	case errors.Is(err, store.ErrReportArchiveProtected):
		fail(w, 409, "归档仍有保留保护、未解除的hold或尚未到期，不能放行轮换或级联删除。")
	case errors.Is(err, store.ErrReportArchiveFailed):
		fail(w, 422, "这次采集的报告归档生成失败。")
	default:
		operationsFailure(w, err)
	}
}

func (s *server) reportArchivePreview(w http.ResponseWriter, r *http.Request, user store.User, workspace store.Workspace) {
	id, err := strconv.ParseInt(r.PathValue("archive"), 10, 64)
	if err != nil || id < 1 {
		fail(w, http.StatusBadRequest, "报告归档编号无效。")
		return
	}
	raw, integrity, err := s.store.ReportArchiveVerifiedBytes(r.Context(), workspace.ID, user.ID, id)
	if err != nil {
		reportArchiveFailure(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Archive-Digest", integrity.ContentDigest)
	_, _ = w.Write(raw)
}

func (s *server) reportArchiveExport(w http.ResponseWriter, r *http.Request, user store.User, workspace store.Workspace) {
	format, ok := reportExportFormat(w, r)
	if !ok {
		return
	}
	id, ok := operationPathID(w, r, "archive")
	if !ok {
		return
	}
	raw, integrity, err := s.store.ReportArchiveVerifiedBytes(r.Context(), workspace.ID, user.ID, id)
	if err != nil {
		reportArchiveFailure(w, err)
		return
	}
	contentType := "application/json; charset=utf-8"
	filename := "scarlet-compliance-report.json"
	if format != "json" {
		format = "html"
		var report store.ComplianceReport
		if json.Unmarshal(raw, &report) != nil {
			reportArchiveFailure(w, store.ErrReportArchiveIntegrity)
			return
		}
		var b bytes.Buffer
		if err = complianceHTML.Execute(&b, report); err != nil {
			fail(w, 500, "无法生成报告。")
			return
		}
		raw = b.Bytes()
		contentType = "text/html; charset=utf-8"
		filename = "scarlet-compliance-report.html"
	}
	digest := sha256.Sum256(raw)
	receipt, err := s.store.RecordReportArchiveExport(r.Context(), workspace.ID, user.ID, id, integrity.ContentDigest, format, hex.EncodeToString(digest[:]), int64(len(raw)))
	if err != nil {
		reportArchiveFailure(w, err)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("Content-Digest", "sha-256=:"+base64.StdEncoding.EncodeToString(digest[:])+":")
	w.Header().Set("X-Archive-Digest", integrity.ContentDigest)
	w.Header().Set("X-Export-Receipt-Id", strconv.FormatInt(receipt, 10))
	if format == "html" {
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; sandbox")
	}
	_, _ = w.Write(raw)
}

func (s *server) reportArchiveRetentionRoutes(mux *http.ServeMux) {
	const base = "/api/v1/workspaces/{slug}/reports/archives/{archive}"
	mux.HandleFunc("POST "+base+"/retention", s.workspace(false, s.setReportArchiveRetention))
	mux.HandleFunc("GET "+base+"/retention-history", s.workspace(false, s.reportArchiveRetentionHistory))
	mux.HandleFunc("GET "+base+"/integrity", s.workspace(false, s.reportArchiveIntegrity))
}
func (s *server) setReportArchiveRetention(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	id, ok := operationPathID(w, r, "archive")
	if !ok {
		return
	}
	var in store.ReportArchiveRetentionInput
	if !readJSONLimit(w, r, &in, 8<<10) {
		return
	}
	out, err := s.store.SetReportArchiveRetention(r.Context(), ws.ID, u.ID, id, in)
	if err != nil {
		reportArchiveFailure(w, err)
		return
	}
	respond(w, 200, out)
}
func (s *server) reportArchiveRetentionHistory(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	id, ok := operationPathID(w, r, "archive")
	if !ok {
		return
	}
	cursor, limit, ok := governanceOpsPagination(w, r)
	if !ok {
		return
	}
	out, err := s.store.ReportArchiveRetentionHistory(r.Context(), ws.ID, u.ID, id, cursor, limit)
	if err != nil {
		reportArchiveFailure(w, err)
		return
	}
	respond(w, 200, out)
}
func (s *server) reportArchiveIntegrity(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	id, ok := operationPathID(w, r, "archive")
	if !ok {
		return
	}
	_, out, err := s.store.ReportArchiveVerifiedBytes(r.Context(), ws.ID, u.ID, id)
	if err != nil {
		reportArchiveFailure(w, err)
		return
	}
	respond(w, 200, out)
}
