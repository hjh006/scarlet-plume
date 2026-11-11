package main

import (
	"bytes"
	"context"
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
	"syscall"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/apivalidator"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func main() {
	endpoint := flag.String("url", "", "Scarlet platform origin")
	manifestFile := flag.String("manifest", "", "local approved test manifest")
	state := flag.String("state", "/state/pending.json", "source-local sanitized receipt spool")
	targetCA := flag.String("target-ca", "", "optional target private CA")
	local := flag.Bool("local", false, "allow local HTTP platform endpoint")
	once := flag.Bool("once", false, "claim at most one due plan")
	flag.Parse()
	parsed, err := url.Parse(*endpoint)
	if err != nil || parsed.User != nil || parsed.Host == "" || parsed.Path != "" && parsed.Path != "/" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Scheme != "https" && !(parsed.Scheme == "http" && *local) || os.Getenv("SCARLET_VALIDATION_SOURCE_TOKEN") == "" || *manifestFile == "" {
		fmt.Fprintln(os.Stderr, "configure platform origin, source token and local manifest")
		os.Exit(1)
	}
	target, err := apivalidator.PrivateClient(*targetCA)
	if err != nil {
		fmt.Fprintln(os.Stderr, "target TLS configuration failed")
		os.Exit(1)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		err := cycle(ctx, client, target, parsed.Scheme+"://"+parsed.Host, *manifestFile, *state)
		if err != nil {
			fmt.Fprintln(os.Stderr, "API validation cycle incomplete; local scope, lease or connectivity requires attention")
		}
		if *once {
			if err != nil {
				os.Exit(1)
			}
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func post(ctx context.Context, client *http.Client, endpoint string, payload []byte, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+os.Getenv("SCARLET_VALIDATION_SOURCE_TOKEN"))
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("platform unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("platform rejected validation receipt: status%d", resp.StatusCode)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 65536))
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 65536)).Decode(out)
}
func savePending(name string, payload []byte) error {
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(name), "validation-pending-*")
	if err != nil {
		return err
	}
	temp := file.Name()
	defer os.Remove(temp)
	if err := file.Chmod(0600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(payload); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(temp, name)
}
func cycle(ctx context.Context, platform, target *http.Client, origin, manifestFile, state string) error {
	if pending, err := os.ReadFile(state); err == nil {
		if len(pending) > 16384 {
			return errors.New("oversized local receipt")
		}
		if err := post(ctx, platform, origin+"/api/v1/api-validation/result", pending, nil); err != nil {
			return err
		}
		return os.Remove(state)
	} else if !os.IsNotExist(err) {
		return errors.New("local receipt state unavailable")
	}
	manifest, err := apivalidator.LoadManifest(manifestFile)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]int64{"sourceId": manifest.SourceID})
	var response struct {
		Claim *store.APIValidationClaim `json:"claim"`
	}
	if err := post(ctx, platform, origin+"/api/v1/api-validation/claim", body, &response); err != nil {
		return err
	}
	if response.Claim == nil {
		return nil
	}
	probe, cancel := context.WithTimeout(ctx, 15*time.Second)
	result := apivalidator.Execute(probe, target, manifest, *response.Claim)
	cancel()
	payload, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if err := savePending(state, payload); err != nil {
		return err
	}
	if err := post(ctx, platform, origin+"/api/v1/api-validation/result", payload, nil); err != nil {
		return err
	}
	return os.Remove(state)
}
