// endpoint-agent mediates explicitly requested Windows operations and reports
// native observations. Enterprise installation is explicit, uses fixed native
// control targets and never installs a driver or reads a user's desktop as SYSTEM.
package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/endpointguard"
	"github.com/hjh006/scarlet-plume/backend/internal/endpointnative"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/hjh006/scarlet-plume/backend/pkg/datasafety"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	origin := flag.String("url", "", "fixed HTTPS platform origin")
	local := flag.Bool("local", false, "allow loopback HTTP only")
	mode := flag.String("mode", "heartbeat", "heartbeat, sync, watch, usb, print, clipboard, replay, session-plan, session-watch, session-replay, session-stop or session-copy")
	policyID := flag.Int64("policy", 0, "exact approved policy ID")
	state := flag.String("state-dir", "", "dedicated private journal/outbox directory")
	op := flag.String("operation-id", "", "unique local operation ID; never reuse for a different action")
	from := flag.String("from-root", "", "explicit authorized source root")
	source := flag.String("source", "", "relative bounded source file")
	to := flag.String("usb-root", "", "explicit removable-volume destination root")
	destination := flag.String("destination", "", "relative non-overwriting USB destination")
	printer := flag.String("printer", "", "explicit installed Windows printer")
	enterpriseTemplatePath := flag.String("config", "", "explicit enterprise JSON template for provision")
	expectedExecutableHash := flag.String("expected-sha256", "", "independently approved lowercase executable SHA256 required for service installation")
	flag.Parse()
	if flag.NArg() != 0 {
		return errors.New("unexpected endpoint arguments")
	}
	if *mode == "provision" || *mode == "service-install" || *mode == "service-run" || *mode == "service-remove" || strings.HasPrefix(*mode, "native-") {
		dir, err := enterpriseDirectory()
		if err != nil {
			return err
		}
		switch *mode {
		case "provision":
			return provisionEnterprise(*enterpriseTemplatePath, dir)
		case "service-install":
			return installEnterpriseService(dir, *expectedExecutableHash)
		case "service-run":
			return runEnterpriseService(dir)
		case "service-remove":
			return removeEnterpriseService(dir)
		default:
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
			defer cancel()
			return enterpriseNative(ctx, dir, *mode)
		}
	}
	c, err := newEndpointClient(*origin, *local)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if strings.HasPrefix(*mode, "session-") {
		binding, err := configureClipboardSession(c)
		if err != nil {
			return err
		}
		if *mode == "session-plan" {
			return json.NewEncoder(os.Stdout).Encode(map[string]any{"deviceRef": c.device, "sessionRef": binding.sessionRef, "configDigest": binding.configDigest, "capabilities": c.nativeCapabilities(), "prePasteBlocking": false, "windowsRuntimeAcceptance": "pending"})
		}
		if *state == "" {
			return errors.New("dedicated private per-user state directory required")
		}
		if err := os.MkdirAll(*state, 0700); err != nil {
			return errors.New("session state unavailable")
		}
		if *mode != "session-copy" {
			root, err := os.OpenRoot(*state)
			if err != nil {
				return errors.New("session state unavailable")
			}
			defer root.Close()
			return runClipboardSession(ctx, c, root, binding, *mode)
		}
		// Application integrations call this exact mediated write path. Its
		// preflight denial is separate from the reactive clipboard observer.
		*mode = "clipboard"
	}
	if *mode == "heartbeat" {
		return c.heartbeat(ctx)
	}
	if *state == "" {
		return errors.New("provide a dedicated private state directory")
	}
	if err := os.MkdirAll(*state, 0700); err != nil {
		return errors.New("state directory unavailable")
	}
	root, err := os.OpenRoot(*state)
	if err != nil {
		return errors.New("state directory unavailable")
	}
	defer root.Close()
	if *mode == "replay" {
		return replay(ctx, c, root)
	}
	if runtime.GOOS != "windows" {
		return errors.New("native operations require Windows; use heartbeat or replay only on this OS")
	}
	if err := c.heartbeat(ctx); err != nil {
		return err
	}
	if *mode == "watch" {
		return watch(ctx, c, root)
	}
	p, err := c.policy(ctx, *policyID)
	if err != nil {
		return err
	}
	if *mode == "sync" {
		return c.receipt(ctx, p)
	}
	if p.Channel != *mode || len(*op) < 1 || len(*op) > 128 || strings.ContainsAny(*op, "\r\n\x00") {
		return errors.New("matching channel policy and unique operation ID required")
	}
	if err := c.receipt(ctx, p); err != nil {
		return err
	}
	if !filepath.IsLocal(*source) {
		return errors.New("provide an explicit authorized root and relative source file")
	}
	in, err := os.OpenRoot(*from)
	if err != nil {
		return errors.New("source unavailable")
	}
	defer in.Close()
	f, err := in.Open(*source)
	if err != nil {
		return errors.New("source unavailable")
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil || !stat.Mode().IsRegular() {
		return errors.New("source must be a bounded regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(f, 1<<20+1))
	if err != nil || len(raw) > 1<<20 {
		return errors.New("source exceeds one MiB; operation not started")
	}
	defer clear(raw)
	operationRef := c.ref("operation", *op)
	// O_EXCL intent survives crashes: an uncertain print/clipboard action is
	// never silently retried. Journal files contain metadata only.
	intent, err := root.OpenFile(operationRef+".intent", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errors.New("operation ID already attempted or journal unavailable; inspect its evidence before any new operation")
	}
	_, err = intent.WriteString("endpoint-intent-v1\n")
	if err == nil {
		err = intent.Sync()
	}
	closeErr := intent.Close()
	if err != nil || closeErr != nil {
		return errors.New("durable operation intent unavailable")
	}
	jobRef := ""
	audit := func(fact datasafety.Context) error {
		fact.FileDigest = c.ref("content", string(raw))
		fact.ObjectRef = c.ref("target", *mode+"\x00"+*to+"\x00"+*destination+"\x00"+*printer+"\x00"+jobRef)
		return deliver(ctx, c, root, c.event(operationRef, fact))
	}
	action := func(data []byte) error {
		switch *mode {
		case "usb":
			return endpointnative.CopyUSB(*to, *destination, data)
		case "print":
			var e error
			jobRef, e = endpointnative.PrintText(*printer, data)
			return e
		case "clipboard":
			return endpointnative.WriteClipboard(data)
		}
		return errors.New("unsupported endpoint operation")
	}
	format := sourceFileFormat(*source)
	fact, err := endpointguard.Run(c.guardPolicy(p), endpointguard.Identity{DeviceRef: c.device, SubjectRef: c.subject, OperationRef: operationRef}, format, raw, audit, action)
	if fact.Endpoint != nil {
		fmt.Printf("endpoint operation %s; stage %s; original content retained locally only\n", fact.Enforcement, fact.Endpoint.Stage)
	}
	return err
}

// Preserve the source format for every channel. A JSON file can carry sensitive
// scalars in Unicode escapes even when the submitted bytes contain no '@'.
func sourceFileFormat(source string) string {
	return strings.TrimPrefix(strings.ToLower(filepath.Ext(source)), ".")
}

func deliver(ctx context.Context, c *endpointClient, root *os.Root, event store.SecurityEventInput) error {
	raw, err := json.Marshal(event)
	if err != nil {
		return errors.New("evidence encoding failed")
	}
	name := c.ref("outbox", event.EventID) + ".json"
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errors.New("evidence journal unavailable")
	}
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return errors.New("evidence journal write incomplete")
	}
	if err := c.request(ctx, "POST", "/api/v1/operations/security-events", event, nil); err != nil {
		return err
	}
	if root.Remove(name) != nil {
		return errors.New("event accepted; local evidence pending cleanup")
	}
	return nil
}
func replay(ctx context.Context, c *endpointClient, root *os.Root) error {
	dir, err := root.Open(".")
	if err != nil {
		return errors.New("outbox unavailable")
	}
	defer dir.Close()
	delivered := 0
	for {
		entries, readErr := dir.ReadDir(256)
		if readErr != nil && readErr != io.EOF {
			return errors.New("outbox unavailable")
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			delivered++
			if delivered > 1000 {
				return errors.New("outbox batch limit reached; rerun replay for remaining entries")
			}
			f, err := root.Open(entry.Name())
			if err != nil {
				return errors.New("outbox entry unavailable")
			}
			raw, err := io.ReadAll(io.LimitReader(f, 16<<10+1))
			f.Close()
			var event store.SecurityEventInput
			if err != nil || len(raw) > 16<<10 || json.Unmarshal(raw, &event) != nil || event.DataContext == nil || event.DataContext.Validate() != nil || event.DataContext.DeviceRef != c.device || entry.Name() != c.ref("outbox", event.EventID)+".json" {
				return errors.New("invalid source-bound outbox entry")
			}
			if c.request(ctx, "POST", "/api/v1/operations/security-events", event, nil) != nil {
				return errors.New("outbox delivery pending")
			}
			if root.Remove(entry.Name()) != nil {
				return errors.New("accepted entry cleanup pending")
			}
		}
		if readErr == io.EOF {
			break
		}
	}

	return nil
}
func watch(ctx context.Context, c *endpointClient, root *os.Root) error {
	// Native sequence counters are local to one observer run. A fresh stream
	// identity prevents a restart or another printer queue from reusing an
	// already accepted event ID with a different timestamp or target.
	stream := rand.Text()
	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	heartbeats := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-watchCtx.Done():
				return
			case <-ticker.C:
				if err := c.heartbeat(watchCtx); err != nil {
					heartbeats <- err
					cancel()
					return
				}
			}
		}
	}()
	err := endpointnative.Watch(watchCtx, func(o endpointnative.Observation) error {
		fact := datasafety.Context{Kind: "endpoint", Channel: o.Channel, Operation: "change", Enforcement: "observed", Capture: "partial", Categories: []string{}, DeviceRef: c.device, SubjectRef: c.subject}
		if o.Complete {
			classified, _, e := datasafety.InspectText(strings.NewReader(string(o.Text)))
			if e == nil {
				fact.Categories = classified.Categories
			}
		}
		if o.RawTarget != "" {
			fact.ObjectRef = c.ref("observed-object", o.RawTarget)
		}
		ref := c.observationRef(stream, o.Channel, o.Kind, o.Sequence, o.RawTarget)
		executor := map[string]string{"usb": "windows_usb_volume", "print": "windows_print_spool", "clipboard": "windows_clipboard_listener"}[o.Channel]
		fact.Endpoint = &datasafety.EndpointEvidence{Version: 1, Scope: "observation", Stage: "observed", Executor: executor, OperationRef: ref}
		if o.Kind == "observer_unavailable" {
			fact.Enforcement = "unknown"
			fact.Capture = "unknown"
		}
		event := c.event(ref, fact)
		event.Rule = "endpoint:" + o.Kind
		return deliver(watchCtx, c, root, event)
	})
	select {
	case e := <-heartbeats:
		return e
	default:
		return err
	}
}
