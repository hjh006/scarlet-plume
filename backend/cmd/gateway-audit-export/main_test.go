package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hjh006/scarlet-plume/backend/pkg/gatewaygovernance"
)

func exportTestEvents(n int) []gatewaygovernance.AuditEvent {
	out := []gatewaygovernance.AuditEvent{}
	previous := ""
	for i := 1; i <= n; i++ {
		e := gatewaygovernance.AuditEvent{Sequence: int64(i), WorkspaceID: 42, ActorID: 7, Action: "gateway.call_reserved", Target: "reservation", Payload: json.RawMessage(`{"reservedMicros":10}`), OccurredAt: time.Unix(int64(i), 0).UTC(), PreviousHash: previous}
		e.Hash = gatewaygovernance.HashEvent(e)
		previous = e.Hash
		out = append(out, e)
	}
	return out
}
func exportTestPage(events []gatewaygovernance.AuditEvent, after int64, limit int) gatewaygovernance.AuditPage {
	previous := ""
	if after > 0 {
		previous = events[after-1].Hash
	}
	end := min(int(after)+limit, len(events))
	items := events[int(after):end]
	head := ""
	if len(events) > 0 {
		head = events[len(events)-1].Hash
	}
	return gatewaygovernance.AuditPage{WorkspaceID: 42, Items: items, NextCursor: int64(end), Valid: true, PreviousHash: previous, HeadHash: head, HeadSequence: int64(len(events))}
}
func TestExportRecoveryAfterInterruptedCheckpointAndPartialLine(t *testing.T) {
	events := exportTestEvents(3)
	token := strings.Repeat("t", 40)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/v1/internal/ai-gateway/audit/export" || r.Header.Get("Authorization") != "Bearer "+token || r.URL.Query().Get("workspaceSlug") != "test-workspace" {
			t.Error("request binding invalid")
			w.WriteHeader(401)
			return
		}
		after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		_ = json.NewEncoder(w).Encode(exportTestPage(events, after, limit))
	}))
	defer server.Close()
	dir := t.TempDir()
	output := filepath.Join(dir, "audit.jsonl")
	cp := output + ".checkpoint"
	writer, e := openExporter(server.URL, "test-workspace", output, cp, token, server.Client())
	if e != nil {
		t.Fatal(e)
	}
	initialCheckpoint := writer.position
	writer.pageSize = 2
	if e = writer.catchUp(t.Context()); e != nil {
		t.Fatal(e)
	}
	if writer.position.Sequence != 3 || calls != 2 {
		t.Fatalf("position=%d calls=%d", writer.position.Sequence, calls)
	}
	writer.file.Close()
	if e = saveCheckpoint(cp, initialCheckpoint); e != nil {
		t.Fatal(e)
	}
	f, e := os.OpenFile(output, os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		t.Fatal(e)
	}
	_, _ = f.WriteString(`{"sequence":4,"partial":`)
	f.Close()
	recovered, e := openExporter(server.URL, "test-workspace", output, cp, token, server.Client())
	if e != nil {
		t.Fatal(e)
	}
	defer recovered.file.Close()
	if recovered.position.Sequence != 3 {
		t.Fatalf("recovered position %d", recovered.position.Sequence)
	}
	if e = recovered.catchUp(t.Context()); e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile(output)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Count(string(raw), "\n") != 3 || strings.Contains(string(raw), "partial") {
		t.Fatal("recovery lost or duplicated complete records")
	}
	if _, e = openExporter(server.URL, "test-workspace", output, cp, token, server.Client()); e == nil {
		t.Fatal("simultaneous writer accepted")
	}
}
func TestExportRefusesBrokenPageThenResumesWithoutGap(t *testing.T) {
	events := exportTestEvents(3)
	broken := true
	token := strings.Repeat("t", 40)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		page := exportTestPage(events, after, limit)
		if after == 2 && broken {
			copyItems := append([]gatewaygovernance.AuditEvent{}, page.Items...)
			copyItems[0].Payload = json.RawMessage(`{"reservedMicros":0}`)
			page.Items = copyItems
		}
		_ = json.NewEncoder(w).Encode(page)
	}))
	defer server.Close()
	output := filepath.Join(t.TempDir(), "audit.jsonl")
	writer, e := openExporter(server.URL, "workspace", output, output+".checkpoint", token, server.Client())
	if e != nil {
		t.Fatal(e)
	}
	writer.pageSize = 2
	if e = writer.catchUp(t.Context()); e == nil {
		t.Fatal("corrupt page accepted")
	}
	if writer.position.Sequence != 2 {
		t.Fatal("checkpoint advanced past corrupt page")
	}
	writer.file.Close()
	cp, _, e := loadCheckpoint(output + ".checkpoint")
	if e != nil || cp.Sequence != 2 {
		t.Fatalf("durable checkpoint %+v %v", cp, e)
	}
	broken = false
	recovered, e := openExporter(server.URL, "workspace", output, output+".checkpoint", token, server.Client())
	if e != nil {
		t.Fatal(e)
	}
	defer recovered.file.Close()
	if e = recovered.catchUp(t.Context()); e != nil {
		t.Fatal(e)
	}
	raw, _ := os.ReadFile(output)
	if strings.Count(string(raw), "\n") != 3 {
		t.Fatal("resume duplicated or skipped an event")
	}
}
func TestExportRejectsTruncationSourceChangeAndUnsafeFiles(t *testing.T) {
	token := strings.Repeat("t", 40)
	output := filepath.Join(t.TempDir(), "audit.jsonl")
	cp := output + ".checkpoint"
	writer, e := openExporter("https://controller.invalid", "workspace", output, cp, token, http.DefaultClient)
	if e != nil {
		t.Fatal(e)
	}
	if e = writer.appendPage(exportTestPage(exportTestEvents(2), 0, 200)); e != nil {
		t.Fatal(e)
	}
	writer.file.Close()
	if _, e = openExporter("https://other.invalid", "workspace", output, cp, token, http.DefaultClient); e == nil {
		t.Fatal("changed platform accepted")
	}
	if _, e = openExporter("https://controller.invalid", "other", output, cp, token, http.DefaultClient); e == nil {
		t.Fatal("changed workspace accepted")
	}
	if e = os.Truncate(output, 0); e != nil {
		t.Fatal(e)
	}
	if _, e = openExporter("https://controller.invalid", "workspace", output, cp, token, http.DefaultClient); e == nil {
		t.Fatal("archive truncation accepted")
	}
	unsafe := filepath.Join(t.TempDir(), "public.jsonl")
	if e = os.WriteFile(unsafe, nil, 0644); e != nil {
		t.Fatal(e)
	}
	// Set the intentionally unsafe mode explicitly; a private process umask can
	// otherwise make the fixture 0600 before the exporter ever sees it.
	if e = os.Chmod(unsafe, 0644); e != nil {
		t.Fatal(e)
	}
	if _, e = openExporter("https://controller.invalid", "workspace", unsafe, unsafe+".checkpoint", token, http.DefaultClient); e == nil {
		t.Fatal("public file accepted")
	}
	link := filepath.Join(t.TempDir(), "linked.jsonl")
	if e = os.Symlink(unsafe, link); e != nil {
		t.Fatal(e)
	}
	if _, e = openExporter("https://controller.invalid", "workspace", link, link+".checkpoint", token, http.DefaultClient); e == nil {
		t.Fatal("symlink accepted")
	}
}
func TestExportTransientFailureAndPageAnchorCannotAdvance(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer server.Close()
	output := filepath.Join(t.TempDir(), "audit.jsonl")
	writer, e := openExporter(server.URL, "workspace", output, output+".checkpoint", strings.Repeat("t", 40), server.Client())
	if e != nil {
		t.Fatal(e)
	}
	defer writer.file.Close()
	if e = writer.catchUp(t.Context()); !errors.Is(e, errTemporary) {
		t.Fatalf("error=%v", e)
	}
	if writer.position.Sequence != 0 {
		t.Fatal("transient failure advanced checkpoint")
	}
	page := exportTestPage(exportTestEvents(2), 0, 200)
	page.Items = page.Items[:1]
	page.NextCursor = 1
	if e = writer.appendPage(page); e == nil {
		t.Fatal("missing tail accepted")
	}
	if writer.position.Sequence != 0 {
		t.Fatal("invalid page advanced checkpoint")
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if waitExport(cancelled, time.Hour) {
		t.Fatal("cancelled exporter still waiting")
	}
}
func TestExportOriginValidation(t *testing.T) {
	for _, url := range []string{"http://remote.invalid", "https://example.invalid/path", "https://user:secret@example.invalid", "https://example.invalid?q=x"} {
		if _, e := exportOrigin(url, true); e == nil {
			t.Fatal("unsafe origin accepted")
		}
	}
	if _, e := exportOrigin("http://127.0.0.1:8080", true); e != nil {
		t.Fatal(e)
	}
}
