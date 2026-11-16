package aisecurity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// These deterministic recognizer-output fixtures exercise malformed or uncertain
// reports. Actual OCR/ASR interoperability is validated separately with the real
// Tesseract/whisper binaries and approved synthetic image/audio fixtures.
func recognitionOutputFixture(t *testing.T, script string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "synthetic-recognizer")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script), 0700); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestMediaSafetyOCRUncertaintyCannotBeAveragedAway(t *testing.T) {
	for _, tc := range []struct {
		name, rows string
		confidence float64
		invalid    bool
	}{
		{"low_word", strings.Repeat("5\t1\t1\t1\t1\t1\t0\t0\t1\t1\t99\tapproved\n", 30) + "5\t1\t1\t1\t1\t1\t0\t0\t1\t1\t20\tuncertain\n", 20, false},
		{"structure_and_empty", "1\t1\t0\t0\t0\t0\t0\t0\t1\t1\t-1\t\n5\t1\t1\t1\t1\t1\t0\t0\t1\t1\t-1\t   \n5\t1\t1\t1\t1\t1\t0\t0\t1\t1\t96\tapproved\n", 96, false},
		{"empty", "5\t1\t1\t1\t1\t1\t0\t0\t1\t1\t-1\t\n", 0, false},
		{"nan", "5\t1\t1\t1\t1\t1\t0\t0\t1\t1\tNaN\tuncertain\n", 0, true},
		{"infinite", "5\t1\t1\t1\t1\t1\t0\t0\t1\t1\t+Inf\tuncertain\n", 0, true},
		{"negative_word", "5\t1\t1\t1\t1\t1\t0\t0\t1\t1\t-1\tuncertain\n", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binary := recognitionOutputFixture(t, "cat <<'TSV'\nlevel\tpage_num\tblock_num\tpar_num\tline_num\tword_num\tleft\ttop\twidth\theight\tconf\ttext\n"+tc.rows+"TSV\n")
			processor, err := NewCommandMediaProcessor(CommandMediaOptions{Tesseract: binary})
			if err != nil {
				t.Fatal(err)
			}
			asset := MediaAsset{MIMEType: "image/png", Data: testPNG()}
			asset.Digest = mediaHash(asset.Data)
			report, err := processor(t.Context(), asset)
			if tc.invalid {
				if err == nil {
					t.Fatal("invalid OCR confidence accepted", report)
				}
				return
			}
			if err != nil || !report.Complete || report.Confidence != tc.confidence {
				t.Fatal("confidence diluted or structural token treated as text", report, err)
			}
			_, categories, err := inspectMediaAssets(t.Context(), []MediaAsset{asset}, processor)
			if err != nil || strings.Contains(strings.Join(categories, ","), "unclassified_media") != (tc.confidence < 90) {
				t.Fatal("uncertainty did not reach data policy", categories, err)
			}
		})
	}
}

func TestMediaSafetyASRUncertaintyAndControlTokens(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		t.Run(fmt.Sprint(malformed), func(t *testing.T) {
			tokens := []map[string]any{{"text": "[_BEG_]", "p": 0}, {"text": " ", "p": 0}}
			for range 30 {
				tokens = append(tokens, map[string]any{"text": " approved", "p": 0.99})
			}
			probability := 0.12
			if malformed {
				probability = 1.01
			}
			tokens = append(tokens, map[string]any{"text": " uncertain", "p": probability})
			output, _ := json.Marshal(map[string]any{"transcription": []any{map[string]any{"text": "approved uncertain", "tokens": tokens}}})
			binary := recognitionOutputFixture(t, "while [ \"$#\" -gt 0 ]; do if [ \"$1\" = '-of' ]; then out=\"$2\"; shift 2; else shift; fi; done\ncat > \"$out.json\" <<'ASR'\n"+string(output)+"\nASR\n")
			model := filepath.Join(t.TempDir(), "synthetic-model-contract.bin")
			data := bytes.Repeat([]byte("x"), 1024)
			if err := os.WriteFile(model, data, 0600); err != nil {
				t.Fatal(err)
			}
			processor, err := NewCommandMediaProcessor(CommandMediaOptions{Whisper: binary, WhisperModel: model, WhisperModelSHA256: mediaHash(data)})
			if err != nil {
				t.Fatal(err)
			}
			asset := MediaAsset{MIMEType: "audio/wav", Data: []byte("synthetic-command-boundary-not-real-recognition")}
			asset.Digest = mediaHash(asset.Data)
			report, err := processor(t.Context(), asset)
			if malformed {
				if err == nil {
					t.Fatal("out-of-range speech confidence accepted")
				}
				return
			}
			if err != nil || !report.Complete || math.Abs(report.Confidence-12) > 0.001 {
				t.Fatal("speech uncertainty was diluted", report, err)
			}
		})
	}
}

func inspectionDepthRequest(t *testing.T, asset MediaAsset, ctx context.Context) *http.Request {
	t.Helper()
	raw, err := json.Marshal(asset)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/inspect", bytes.NewReader(raw)).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer "+sourceTestToken)
	r.Header.Set("Content-Type", "application/json")
	return r
}

func TestMediaSafetyScannerCapacityCancellationAndFailure(t *testing.T) {
	asset, err := canonicalMedia("image/png", testPNG(), testMediaPolicy())
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	waitEntered := func() {
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatal("scanner did not acquire available capacity")
		}
	}
	var calls atomic.Int32
	processor := func(ctx context.Context, a MediaAsset) (MediaReport, error) {
		calls.Add(1)
		entered <- struct{}{}
		select {
		case <-ctx.Done():
			return MediaReport{}, ctx.Err()
		case <-release:
			return MediaReport{Digest: a.Digest, MIMEType: a.MIMEType, InspectionVersion: MediaInspectionVersion, Complete: true, Text: "synthetic capacity fixture", Confidence: 99}, nil
		}
	}
	handler, err := NewMediaInspectionHandler(processor, sourceTestToken)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var cancels []context.CancelFunc
	defer func() {
		for _, cancel := range cancels {
			cancel()
		}
	}()
	for range 4 {
		ctx, cancel := context.WithCancel(t.Context())
		cancels = append(cancels, cancel)
		wg.Add(1)
		go func() {
			defer wg.Done()
			handler.ServeHTTP(httptest.NewRecorder(), inspectionDepthRequest(t, asset, ctx))
		}()
	}
	for range 4 {
		waitEntered()
	}
	busy := httptest.NewRecorder()
	handler.ServeHTTP(busy, inspectionDepthRequest(t, asset, t.Context()))
	if busy.Code != 429 || busy.Header().Get("Retry-After") != "1" || calls.Load() != 4 {
		t.Fatal("scanner did not refuse excess work immediately", busy.Code, calls.Load())
	}
	scanner := httptest.NewServer(handler)
	defer scanner.Close()
	client, err := NewHTTPMediaProcessor(scanner.URL+"/v1/inspect", sourceTestToken, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client(t.Context(), asset); err == nil {
		t.Fatal("rate-limited scanner became successful inspection")
	}
	var providerHits atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { providerHits.Add(1); _, _ = io.WriteString(w, `{}`) }))
	defer provider.Close()
	c := fixture()
	c.Models[0].Media = testMediaPolicy()
	c.Policy.AllowedInputCategories = append(c.Policy.AllowedInputCategories, "image")
	c.Models[0].AllowedInputCategories = append(c.Models[0].AllowedInputCategories, "image")
	runtime, _ := runtimeFixture(t, func(context.Context) (Config, error) { return c, nil }, provider.URL, nil, nil)
	runtime.(*runtimeHandler).options.MediaProcessor = client
	r := leaseModelRequest("media-capacity-policy-0001")
	r.TaskID = ""
	r.Input = mediaInput(asset.Data, "image/png")
	if out := callRuntime(t, runtime, aliceToken, wire(r)); out.Code != 403 || providerHits.Load() != 0 {
		t.Fatal("scanner overload bypassed gateway data inspection", out.Code, out.Body.String(), providerHits.Load())
	}
	for _, cancel := range cancels {
		cancel()
	}
	wg.Wait()
	if calls.Load() != 4 {
		t.Fatal("rejected request started recognition")
	}
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out := httptest.NewRecorder()
			handler.ServeHTTP(out, inspectionDepthRequest(t, asset, t.Context()))
			if out.Code != 200 {
				t.Error("released slot unavailable", out.Code)
			}
		}()
	}
	for range 4 {
		waitEntered()
	}
	unblock()
	wg.Wait()
	if calls.Load() != 8 {
		t.Fatal("cancelled inspections leaked slots")
	}
	failed, _ := NewMediaInspectionHandler(func(context.Context, MediaAsset) (MediaReport, error) {
		return MediaReport{}, errors.New("controlled inspection failure")
	}, sourceTestToken)
	for range 6 {
		out := httptest.NewRecorder()
		failed.ServeHTTP(out, inspectionDepthRequest(t, asset, t.Context()))
		if out.Code != 422 {
			t.Fatal("failed inspections leaked capacity", out.Code)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	out := httptest.NewRecorder()
	handler.ServeHTTP(out, inspectionDepthRequest(t, asset, ctx))
	if out.Code != 408 || calls.Load() != 8 {
		t.Fatal("cancelled request consumed recognition", out.Code, calls.Load())
	}
}

func TestMediaSafetyLegacyCatalogDoesNotTrustLegacyScanner(t *testing.T) {
	asset, err := canonicalMedia("image/png", testPNG(), testMediaPolicy())
	if err != nil {
		t.Fatal(err)
	}
	c := fixture()
	c.Models[0].Media = testMediaPolicy()
	c.Models[0].Media.InspectionVersion = LegacyMediaInspectionVersion
	if err := ValidateConfig(c); err != nil {
		t.Fatal("historical catalog became unreadable", err)
	}
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(MediaReport{Digest: asset.Digest, MIMEType: asset.MIMEType, InspectionVersion: LegacyMediaInspectionVersion, Complete: true, Text: "legacy average confidence", Confidence: 99})
	}))
	defer old.Close()
	client, err := NewHTTPMediaProcessor(old.URL, sourceTestToken, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client(t.Context(), asset); err == nil {
		t.Fatal("legacy average confidence became v2 minimum confidence")
	}
	var hits atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1); _, _ = io.WriteString(w, `{}`) }))
	defer up.Close()
	runtime, _ := runtimeFixture(t, func(context.Context) (Config, error) { return c, nil }, up.URL, nil, nil)
	runtime.(*runtimeHandler).options.MediaProcessor = client
	r := testRequest("model")
	r.ModelID = "enterprise"
	r.Input = mediaInput(asset.Data, "image/png")
	out := callRuntime(t, runtime, aliceToken, wire(r))
	if out.Code != 403 || hits.Load() != 0 || !strings.Contains(out.Body.String(), "media_input_inspection_required") {
		t.Fatal("old scanner allowed execution under a historical catalog", out.Code, out.Body.String(), hits.Load())
	}
	invalid, _ := NewMediaInspectionHandler(func(context.Context, MediaAsset) (MediaReport, error) {
		return MediaReport{Digest: asset.Digest, MIMEType: asset.MIMEType, InspectionVersion: LegacyMediaInspectionVersion, Complete: true, Confidence: 99}, nil
	}, sourceTestToken)
	rejected := httptest.NewRecorder()
	invalid.ServeHTTP(rejected, inspectionDepthRequest(t, asset, t.Context()))
	if rejected.Code != 422 || !strings.Contains(rejected.Body.String(), "inspection_receipt_invalid") {
		t.Fatal("new scanner served an invalid old-version receipt", rejected.Code, rejected.Body.String())
	}
}
