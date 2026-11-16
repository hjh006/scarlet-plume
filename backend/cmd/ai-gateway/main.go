// ai-gateway runs the Scarlet safety boundary in front of a private, fixed
// Bifrost OSS deployment. It never exposes Bifrost's management or proxy API.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/mediaprobe"
	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
)

func privateFile(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("private deployment file unavailable")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > limit {
		return nil, errors.New("deployment files must be private bounded regular files")
	}
	return io.ReadAll(io.LimitReader(f, limit+1))
}
func decode(path string, limit int64, dst any) error {
	raw, err := privateFile(path, limit)
	if err != nil {
		return err
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if d.Decode(dst) != nil {
		return errors.New("deployment JSON invalid")
	}
	var trailing any
	if d.Decode(&trailing) != io.EOF {
		return errors.New("deployment trailing JSON")
	}
	return nil
}

func run() error {
	listen := flag.String("listen", "127.0.0.1:8905", "Scarlet gateway listener")
	catalogPath := flag.String("catalog", "", "private trusted catalog JSON")
	bindingsPath := flag.String("bindings", "", "private runtime target/credential/tool bindings")
	profilePath := flag.String("bifrost-profile", "", "private fixed Bifrost profile")
	docPath := flag.String("documents", "", "private local document snapshot")
	docSourceURL := flag.String("document-source-url", "", "fixed private HTTPS authorized document source")
	docSourceToken := flag.String("document-source-token-file", "", "private document source token file")
	auditPath := flag.String("audit", "", "durable private content-free audit JSONL")
	journalPath := flag.String("journal", "", "durable exclusive nonce journal")
	binary := flag.String("bifrost-binary", "", "managed Bifrost OSS binary (required outside synthetic mode)")
	controlURL := flag.String("control-url", "", "fixed HTTPS governance controller URL")
	controlTokenPath := flag.String("control-token-file", "", "private node-to-controller token file")
	workspace := flag.String("workspace", "", "controller-bound workspace slug")
	defaultNode, _ := os.Hostname()
	nodeID := flag.String("node-id", defaultNode, "stable controller-scoped node identifier")
	cert := flag.String("tls-cert", "", "public HTTPS certificate")
	key := flag.String("tls-key", "", "public HTTPS private key")
	mediaURL := flag.String("media-inspector-url", os.Getenv("SCARLET_GATEWAY_MEDIA_INSPECTOR_URL"), "fixed private media inspection endpoint")
	mediaTokenFile := flag.String("media-inspector-token-file", "", "private media inspection token")
	mediaCA := flag.String("media-inspector-ca-file", os.Getenv("SCARLET_GATEWAY_MEDIA_INSPECTOR_CA_FILE"), "additional public CA for scanner TLS only")
	healthListen := flag.String("health-listen", "127.0.0.1:8906", "private loopback health listener")
	checkReady := flag.Bool("check-ready", false, "check existing gateway readiness without loading credentials or journals")
	tesseract := flag.String("media-tesseract", "", "absolute Tesseract executable")
	whisper := flag.String("media-whisper", "", "absolute whisper-cli executable")
	whisperModel := flag.String("media-whisper-model", "", "private pinned speech model")
	whisperHash := flag.String("media-whisper-model-sha256", "", "pinned speech model SHA256")
	mediaLanguages := flag.String("media-languages", "eng", "installed OCR languages")
	local := flag.Bool("local", false, "synthetic loopback HTTP test mode")
	flag.Parse()
	if flag.NArg() != 0 {
		return errors.New("unexpected gateway arguments")
	}
	if readinessAddress(*healthListen) != nil {
		return errors.New("private loopback health listener required")
	}
	if *checkReady {
		return checkReadiness(*healthListen)
	}
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,79}$`).MatchString(*nodeID) {
		return errors.New("valid node-id required")
	}
	if *catalogPath == "" || *bindingsPath == "" || *profilePath == "" || *auditPath == "" || *journalPath == "" {
		return errors.New("catalog, bindings, bifrost-profile, audit and journal required")
	}
	host, _, err := net.SplitHostPort(*listen)
	if err != nil {
		return errors.New("invalid gateway listener")
	}
	if *local {
		if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
			return errors.New("synthetic listener must be literal loopback")
		}
	} else if *cert == "" || *key == "" || *binary == "" || *controlURL == "" || *controlTokenPath == "" || *workspace == "" {
		return errors.New("production requires HTTPS, managed Bifrost and remote governance")
	}
	profiles, err := loadProfiles(*profilePath, *local)
	if err != nil {
		return err
	}
	var bindings struct {
		Credentials []aisecurity.Credential `json:"credentials"`
		Targets     map[string]struct {
			URL              string `json:"url"`
			AuthorizationEnv string `json:"authorizationEnv,omitempty"`
		} `json:"targets"`
		Tools []aisecurity.GatewayTool `json:"tools"`
	}
	if err := decode(*bindingsPath, 1<<20, &bindings); err != nil {
		return err
	}
	targets := map[string]aisecurity.Target{}
	for name, binding := range bindings.Targets {
		auth := ""
		if binding.AuthorizationEnv != "" {
			token := os.Getenv(binding.AuthorizationEnv)
			if len(token) < 16 || len(token) > 4096 {
				return errors.New("target authorization environment unavailable")
			}
			auth = "Bearer " + token
		}
		targets[name] = aisecurity.Target{URL: binding.URL, Authorization: auth}
	}
	catalog := func(context.Context) (aisecurity.Config, error) {
		var c aisecurity.Config
		if err := decode(*catalogPath, 4<<20, &c); err != nil {
			return c, err
		}
		return c, aisecurity.ValidateBifrostCatalogProfiles(profiles, c, targets)
	}
	if _, err := catalog(context.Background()); err != nil {
		return err
	}
	journal, err := aisecurity.OpenReplayJournal(*journalPath)
	if err != nil {
		return errors.New("exclusive replay journal unavailable")
	}
	defer journal.Close()
	appendAudit, closeAudit, err := aisecurity.AppendAudit(*auditPath)
	if err != nil {
		return err
	}
	defer closeAudit()
	var documents func(context.Context) (map[string]aisecurity.LocalDocument, error)
	var resolveDocuments aisecurity.DocumentSource
	if *docSourceURL != "" {
		if *docPath != "" {
			return errors.New("choose one document source deployment")
		}
		raw, err := privateFile(*docSourceToken, 4096)
		if err != nil {
			return err
		}
		if *workspace != "" {
			resolveDocuments, err = aisecurity.NewHTTPDocumentSourceForWorkspace(*docSourceURL, strings.TrimSpace(string(raw)), *workspace, *local)
		} else {
			resolveDocuments, err = aisecurity.NewHTTPDocumentSource(*docSourceURL, strings.TrimSpace(string(raw)), *local)
		}
		if err != nil {
			return err
		}
	} else if *docSourceToken != "" {
		return errors.New("document source endpoint required")
	}
	if *docPath != "" {
		documents = func(context.Context) (map[string]aisecurity.LocalDocument, error) {
			var rows []aisecurity.LocalDocument
			if err := decode(*docPath, 8<<20, &rows); err != nil {
				return nil, err
			}
			out := map[string]aisecurity.LocalDocument{}
			for _, row := range rows {
				if _, ok := out[row.ID]; ok {
					return nil, errors.New("duplicate local document")
				}
				out[row.ID] = row
			}
			return out, nil
		}
	}
	var controller *aisecurity.GatewayController
	if *controlURL != "" {
		raw, err := privateFile(*controlTokenPath, 4096)
		if err != nil {
			return err
		}
		controller, err = aisecurity.NewGatewayController(aisecurity.GatewayControllerOptions{URL: *controlURL, Token: strings.TrimSpace(string(raw)), WorkspaceSlug: *workspace, Profiles: profiles, Catalog: catalog, AllowLoopback: *local})
		if err != nil {
			return err
		}
	}
	var media aisecurity.MediaProcessor
	if *mediaURL != "" {
		if *tesseract != "" || *whisper != "" {
			return errors.New("choose one media inspection deployment")
		}
		token, err := privateFile(*mediaTokenFile, 4096)
		if err != nil {
			return err
		}
		ca, caErr := mediaprobe.LoadCA(*mediaCA)
		if caErr != nil {
			return caErr
		}
		media, err = aisecurity.NewHTTPMediaProcessorWithCA(*mediaURL, strings.TrimSpace(string(token)), *local, ca)
		if err != nil {
			return err
		}
	} else if *tesseract != "" || *whisper != "" {
		media, err = aisecurity.NewCommandMediaProcessor(aisecurity.CommandMediaOptions{Tesseract: *tesseract, Whisper: *whisper, WhisperModel: *whisperModel, WhisperModelSHA256: *whisperHash, Languages: *mediaLanguages})
		if err != nil {
			return err
		}
	}
	initial, err := catalog(context.Background())
	if err != nil {
		return err
	}
	for _, model := range initial.Models {
		if model.Enabled && model.Media != nil && media == nil {
			return errors.New("configured media model requires a ready inspection processor")
		}
	}
	options := aisecurity.RuntimeOptions{ResolveDocuments: resolveDocuments, MediaProcessor: media, Catalog: catalog, Credentials: bindings.Credentials, Targets: targets, Documents: documents, Journal: journal, AllowLoopback: true, Audit: func(ctx context.Context, a aisecurity.AuditRecord) error {
		if err := appendAudit(a); err != nil {
			return err
		}
		if controller != nil {
			return controller.Audit(ctx, a)
		}
		return nil
	}}
	gatewayOptions := aisecurity.GatewayOptions{Runtime: options, Tools: bindings.Tools, BifrostProfiles: profiles}
	if controller != nil {
		gatewayOptions.Runtime.Authenticate = controller.Authenticate
		gatewayOptions.Runtime.TaskControl = controller.TaskControl
		gatewayOptions.BeforeExecute = controller.BeforeExecute
		gatewayOptions.DiscoverTools = controller.DiscoverTools
		gatewayOptions.FallbackModels = controller.FallbackModels
		gatewayOptions.Runtime.Cache = controller
		controller.SetCapabilities(media != nil, documents != nil || resolveDocuments != nil)
	}
	handler, err := aisecurity.NewGateway(gatewayOptions)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	childExit := make(chan error, len(profiles))
	for _, profile := range profiles {
		if *binary != "" {
			dir, err := os.MkdirTemp("", "scarlet-bifrost-")
			if err != nil {
				return err
			}
			defer os.RemoveAll(dir)
			config, err := aisecurity.BifrostConfig(profile, *local)
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(dir, "config.json"), config, 0600); err != nil {
				return err
			}
			child := exec.Command(*binary, "-host", "127.0.0.1", "-port", fmt.Sprint(profile.Port), "-app-dir", dir)
			child.Env = []string{profile.APIKeyEnv + "=" + os.Getenv(profile.APIKeyEnv)}
			for _, name := range []string{"PATH", "SSL_CERT_FILE", "SSL_CERT_DIR"} {
				if value := os.Getenv(name); value != "" {
					child.Env = append(child.Env, name+"="+value)
				}
			}
			child.Env = append(child.Env, "HTTP_PROXY=", "HTTPS_PROXY=", "ALL_PROXY=", "NO_PROXY=*")
			child.Stdout, child.Stderr = os.Stdout, os.Stderr
			if err := child.Start(); err != nil {
				return errors.New("fixed Bifrost process could not start")
			}
			defer func() { _ = child.Process.Kill() }()
			go func() { childExit <- child.Wait() }()
		}
		if err := waitBifrost(ctx, profile.Port, childExit); err != nil {
			return err
		}
	}
	dependencies := &dependencyReadiness{}
	checkDependencies := func(probeCtx context.Context) error {
		for _, profile := range profiles {
			if !bifrostHealthy(probeCtx, profile.Port) {
				return errors.New("Bifrost dependency unavailable")
			}
		}
		c, e := catalog(probeCtx)
		if e != nil {
			return errors.New("catalog dependency unavailable")
		}
		return mediaprobe.Check(probeCtx, media, mediaprobe.Formats(c))
	}
	probeCtx, cancelProbe := context.WithTimeout(ctx, 50*time.Second)
	err = checkDependencies(probeCtx)
	cancelProbe()
	dependencies.record(err)
	if err != nil {
		return errors.New("gateway media/version or transport dependency not ready")
	}
	healthListener, err := net.Listen("tcp", *healthListen)
	if err != nil {
		return errors.New("private health listener unavailable")
	}
	healthServer := &http.Server{Handler: readinessHandler(dependencies), ReadHeaderTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second, IdleTimeout: 5 * time.Second, MaxHeaderBytes: 16384}
	defer healthServer.Close()
	monitorCtx, cancelMonitor := context.WithCancel(ctx)
	monitorDone := make(chan struct{})
	go func() {
		defer close(monitorDone)
		monitorDependencies(monitorCtx, dependencies, checkDependencies, 30*time.Second)
	}()
	defer func() { cancelMonitor(); <-monitorDone }()
	server := &http.Server{Addr: *listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 180 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16384}
	serverExit := make(chan error, 2)
	go func() { serverExit <- healthServer.Serve(healthListener) }()
	go func() {
		if *local {
			serverExit <- server.ListenAndServe()
		} else {
			serverExit <- server.ListenAndServeTLS(*cert, *key)
		}
	}()
	if controller != nil {
		heartbeatCtx, cancelHeartbeats := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			ticker := time.NewTicker(15 * time.Second)
			defer ticker.Stop()
			failures := 0
			for {
				status := "healthy"
				healthy := dependencies.healthy()
				for _, profile := range profiles {
					if !bifrostHealthy(heartbeatCtx, profile.Port) {
						healthy = false
					}
				}
				if !healthy {
					status = "degraded"
					if failures < 1000000 {
						failures++
					}
				} else {
					failures = 0
				}
				_ = controller.Heartbeat(heartbeatCtx, *nodeID, status, failures)
				select {
				case <-heartbeatCtx.Done():
					return
				case <-ticker.C:
				}
			}
		}()
		defer func() {
			cancelHeartbeats()
			<-done
			last, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = controller.Heartbeat(last, *nodeID, "degraded", 0)
		}()
	}
	select {
	case <-ctx.Done():
	case <-childExit:
		err = errors.New("fixed Bifrost process stopped; gateway stopped closed")
	case err = <-serverExit:
	}
	bounded, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = server.Shutdown(bounded)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "ai-gateway:", err)
		os.Exit(1)
	}
}
