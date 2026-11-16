// gateway-audit-export maintains a private, verified JSONL archive. The JSONL
// file is the recovery journal; its checkpoint advances only after fsync.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/hjh006/scarlet-plume/backend/pkg/gatewaygovernance"
)

var errTemporary = errors.New("controller temporarily unavailable; resume from the last verified checkpoint")

type checkpoint struct {
	Version       int    `json:"version"`
	Platform      string `json:"platform"`
	ArchivePath   string `json:"archivePath"`
	WorkspaceSlug string `json:"workspaceSlug"`
	WorkspaceID   int64  `json:"workspaceId"`
	Sequence      int64  `json:"sequence"`
	Hash          string `json:"hash"`
}
type exporter struct {
	file           *os.File
	checkpointPath string
	position       checkpoint
	client         *http.Client
	token          string
	pageSize       int
}

func exportOrigin(value string, local bool) (string, error) {
	u, e := url.Parse(value)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
		return "", errors.New("platform must be an HTTPS origin")
	}
	if u.Scheme != "https" && !(local && u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1")) {
		return "", errors.New("HTTPS required; -local permits only loopback HTTP")
	}
	u.Path = ""
	return strings.TrimRight(u.String(), "/"), nil
}
func strictDecode(raw []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return e
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}
func privateOpen(path string, flags int) (*os.File, error) {
	fd, e := syscall.Open(path, flags|syscall.O_NOFOLLOW, 0600)
	if e != nil {
		return nil, e
	}
	f := os.NewFile(uintptr(fd), path)
	info, e := f.Stat()
	if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		f.Close()
		return nil, errors.New("archive and checkpoint must be private regular files (0600)")
	}
	return f, nil
}
func loadCheckpoint(path string) (checkpoint, bool, error) {
	var c checkpoint
	f, e := privateOpen(path, syscall.O_RDONLY)
	if errors.Is(e, os.ErrNotExist) {
		return c, false, nil
	}
	if e != nil {
		return c, false, e
	}
	defer f.Close()
	raw, e := io.ReadAll(io.LimitReader(f, 16385))
	if e != nil || len(raw) > 16384 {
		return c, false, errors.New("checkpoint unreadable")
	}
	if strictDecode(raw, &c) != nil || c.Version != 1 || c.Sequence < 0 || c.WorkspaceID < 0 {
		return c, false, errors.New("checkpoint invalid")
	}
	return c, true, nil
}
func saveCheckpoint(path string, c checkpoint) error {
	dir := filepath.Dir(path)
	f, e := os.CreateTemp(dir, ".gateway-audit-checkpoint-*")
	if e != nil {
		return e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	raw, e := json.Marshal(c)
	if e == nil {
		_, e = f.Write(append(raw, '\n'))
	}
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	if e = os.Rename(tmp, path); e != nil {
		return e
	}
	d, e := os.Open(dir)
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}
func openExporter(platform, slug, output, checkpointPath, token string, client *http.Client) (*exporter, error) {
	if slug == "" || len(slug) > 128 || strings.ContainsAny(slug, "/\\\r\n\t ") || len(token) < 32 || len(token) > 512 || strings.ContainsAny(token, "\r\n\t ") {
		return nil, errors.New("workspace or control-token environment variable invalid")
	}
	output, e := filepath.Abs(output)
	if e != nil {
		return nil, e
	}
	checkpointPath, e = filepath.Abs(checkpointPath)
	if e != nil {
		return nil, e
	}
	if output == checkpointPath {
		return nil, errors.New("archive and checkpoint paths must differ")
	}
	if e = os.MkdirAll(filepath.Dir(output), 0700); e != nil {
		return nil, e
	}
	if e = os.MkdirAll(filepath.Dir(checkpointPath), 0700); e != nil {
		return nil, e
	}
	f, e := privateOpen(output, syscall.O_CREAT|syscall.O_RDWR)
	if e != nil {
		return nil, e
	}
	success := false
	defer func() {
		if !success {
			f.Close()
		}
	}()
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		return nil, errors.New("another exporter holds this archive")
	}
	existing, exists, e := loadCheckpoint(checkpointPath)
	if e != nil {
		return nil, e
	}
	position := checkpoint{Version: 1, Platform: platform, WorkspaceSlug: slug, ArchivePath: output}
	if exists && (existing.Platform != platform || existing.WorkspaceSlug != slug || existing.ArchivePath != output) {
		return nil, errors.New("checkpoint belongs to another platform or workspace")
	}
	if exists {
		position.WorkspaceID = existing.WorkspaceID
	}
	reader := bufio.NewReaderSize(f, (1<<20)+1)
	offset := int64(0)
	partial := false
	checkpointSeen := !exists || existing.Sequence == 0 && existing.Hash == ""
	for {
		line, readErr := reader.ReadSlice('\n')
		if readErr == io.EOF {
			partial = len(line) > 0
			break
		}
		if readErr != nil {
			return nil, errors.New("archive record unreadable or exceeds 1 MiB")
		}
		var event gatewaygovernance.AuditEvent
		if strictDecode(line, &event) != nil || event.Sequence != position.Sequence+1 || event.PreviousHash != position.Hash || event.Hash != gatewaygovernance.HashEvent(event) || event.WorkspaceID <= 0 || position.WorkspaceID != 0 && event.WorkspaceID != position.WorkspaceID {
			return nil, errors.New("archive hash chain is invalid; checkpoint was not advanced")
		}
		position.WorkspaceID = event.WorkspaceID
		position.Sequence = event.Sequence
		position.Hash = event.Hash
		offset += int64(len(line))
		if exists && event.Sequence == existing.Sequence {
			checkpointSeen = event.Hash == existing.Hash
		}
	}
	if !exists && position.Sequence > 0 {
		return nil, errors.New("existing archive lacks its source-binding checkpoint")
	}
	if exists && (!checkpointSeen || existing.Sequence > position.Sequence) {
		return nil, errors.New("checkpoint exceeds or conflicts with the archive; refusing data loss")
	}
	if partial {
		if e = f.Truncate(offset); e != nil {
			return nil, e
		}
	}
	// Recover complete newline records written before an interrupted checkpoint rename.
	if e = f.Sync(); e != nil {
		return nil, e
	}
	if _, e = f.Seek(0, io.SeekEnd); e != nil {
		return nil, e
	}
	directory, e := os.Open(filepath.Dir(output))
	if e != nil {
		return nil, e
	}
	syncError := directory.Sync()
	directory.Close()
	if syncError != nil {
		return nil, syncError
	}
	if e = saveCheckpoint(checkpointPath, position); e != nil {
		return nil, e
	}
	success = true
	return &exporter{file: f, checkpointPath: checkpointPath, position: position, client: client, token: token, pageSize: 200}, nil
}
func (e *exporter) fetch(ctx context.Context) (gatewaygovernance.AuditPage, error) {
	var page gatewaygovernance.AuditPage
	values := url.Values{"workspaceSlug": {e.position.WorkspaceSlug}, "after": {fmt.Sprint(e.position.Sequence)}, "limit": {fmt.Sprint(e.pageSize)}}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, e.position.Platform+"/api/v1/internal/ai-gateway/audit/export?"+values.Encode(), nil)
	if err != nil {
		return page, err
	}
	request.Header.Set("Authorization", "Bearer "+e.token)
	response, err := e.client.Do(request)
	if err != nil {
		return page, errTemporary
	}
	defer response.Body.Close()
	if response.StatusCode == 429 || response.StatusCode >= 500 {
		return page, errTemporary
	}
	if response.StatusCode != 200 || response.Header.Get("Content-Encoding") != "" {
		return page, errors.New("audit export rejected; check the workspace control credential")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (40<<20)+1))
	if err != nil {
		return page, errTemporary
	}
	if len(raw) > 40<<20 || strictDecode(raw, &page) != nil {
		return page, errors.New("audit export response invalid or too large")
	}
	return page, nil
}
func (e *exporter) appendPage(page gatewaygovernance.AuditPage) error {
	next := e.position
	if !page.Valid || page.WorkspaceID <= 0 || next.WorkspaceID != 0 && next.WorkspaceID != page.WorkspaceID || page.PreviousHash != next.Hash || page.HeadSequence < next.Sequence || len(page.Items) > e.pageSize {
		return errors.New("remote audit anchor is invalid; checkpoint was not advanced")
	}
	next.WorkspaceID = page.WorkspaceID
	var output bytes.Buffer
	for _, event := range page.Items {
		if event.WorkspaceID != next.WorkspaceID || event.Sequence != next.Sequence+1 || event.PreviousHash != next.Hash || event.Hash != gatewaygovernance.HashEvent(event) {
			return errors.New("remote audit chain is invalid; checkpoint was not advanced")
		}
		raw, err := json.Marshal(event)
		if err != nil {
			return err
		}
		output.Write(raw)
		output.WriteByte('\n')
		next.Sequence = event.Sequence
		next.Hash = event.Hash
	}
	if page.NextCursor != next.Sequence || next.Sequence > page.HeadSequence || len(page.Items) < e.pageSize && next.Sequence < page.HeadSequence || next.Sequence == page.HeadSequence && next.Hash != page.HeadHash {
		return errors.New("remote audit page is incomplete; checkpoint was not advanced")
	}
	if output.Len() > 0 {
		if _, err := e.file.Write(output.Bytes()); err != nil {
			return err
		}
		if err := e.file.Sync(); err != nil {
			return err
		}
	}
	if err := saveCheckpoint(e.checkpointPath, next); err != nil {
		return err
	}
	e.position = next
	return nil
}
func (e *exporter) catchUp(ctx context.Context) error {
	for {
		page, err := e.fetch(ctx)
		if err != nil {
			return err
		}
		if err = e.appendPage(page); err != nil {
			return err
		}
		if e.position.Sequence == page.HeadSequence {
			return nil
		}
	}
}
func waitExport(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
func run(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("gateway-audit-export", flag.ContinueOnError)
	platform := flags.String("platform", "", "control-plane HTTPS origin")
	workspace := flags.String("workspace", "", "workspace slug")
	output := flags.String("output", "", "private JSONL archive path")
	cp := flags.String("checkpoint", "", "checkpoint path (defaults to OUTPUT.checkpoint)")
	tokenEnv := flags.String("token-env", "SCARLET_GATEWAY_CONTROL_TOKEN", "environment variable containing workspace node token")
	follow := flags.Bool("follow", false, "continue polling and retry transient controller failures")
	poll := flags.Duration("poll", 15*time.Second, "poll and transient retry interval")
	local := flags.Bool("local", false, "allow loopback HTTP for local tests")
	if err := flags.Parse(args); err != nil {
		return err
	}
	origin, err := exportOrigin(*platform, *local)
	if err != nil {
		return err
	}
	if *output == "" || *workspace == "" || *poll < time.Second || *poll > time.Hour {
		return errors.New("-workspace, -output and a poll interval between 1s and 1h are required")
	}
	if *cp == "" {
		*cp = *output + ".checkpoint"
	}
	client := &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, DisableCompression: true, ResponseHeaderTimeout: 10 * time.Second, MaxResponseHeaderBytes: 16384}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	writer, err := openExporter(origin, *workspace, *output, *cp, os.Getenv(*tokenEnv), client)
	if err != nil {
		return err
	}
	defer writer.file.Close()
	for {
		err = writer.catchUp(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil && (!*follow || !errors.Is(err, errTemporary)) {
			return err
		}
		if err == nil {
			fmt.Fprintf(os.Stderr, "verified archive sequence=%d\n", writer.position.Sequence)
		} else {
			fmt.Fprintln(os.Stderr, err)
		}
		if !*follow {
			return nil
		}
		if !waitExport(ctx, *poll) {
			return nil
		}
	}
}
func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
