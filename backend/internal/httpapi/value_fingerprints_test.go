package httpapi

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/json"
	"fmt"
	"github.com/hjh006/scarlet-plume/backend/internal/runtimesecurity"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"github.com/hjh006/scarlet-plume/backend/internal/valuefingerprint"
	"github.com/hjh006/scarlet-plume/backend/pkg/cryptoevidence"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestValueSearchHTTPOnlyAcceptsTokensAndFindsActualRuntimeEvidence(t *testing.T) {
	dsn := os.Getenv("PLUME_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	ctx := t.Context()
	pool, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	repo := store.New(pool)
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	must(repo.Migrate(ctx))
	slug := fmt.Sprintf("value-http-%d", time.Now().UnixNano())
	must(repo.Bootstrap(ctx, slug, "unused", slug, "Value evidence HTTP"))
	owner, e := repo.FindUser(ctx, slug)
	must(e)
	ws, e := repo.Membership(ctx, owner.ID, slug)
	must(e)
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = pool.Exec(c, `DELETE FROM plume.workspaces WHERE id=$1`, ws.ID)
		_, _ = pool.Exec(c, `DELETE FROM plume.users WHERE id=$1`, owner.ID)
	})
	session, e := sessionToken()
	must(e)
	must(repo.CreateSession(ctx, owner.ID, sessionHash(session), time.Now().Add(time.Hour)))
	runtimeSecret, e := sessionToken()
	must(e)
	runtimeSecret = runtimeTokenPrefix + runtimeSecret
	sourceHash := sessionHash(runtimeSecret)
	source, e := repo.CreateRuntimeSource(ctx, ws.ID, owner.ID, "Source-local capture", "Fixture service", "gateway", sourceHash)
	must(e)
	value := "actual.synthetic@example.invalid"
	key := strings.Repeat("63", 32)
	body := []byte(`{"email":"` + value + `"}`)
	fields, warnings := runtimesecurity.InspectJSON(body, "response")
	fingerprints := runtimesecurity.CaptureValueFingerprints(body, "response", key, "fixture-key", slug)
	_, e = repo.DeliverRuntimeBatch(ctx, "gateway", sourceHash, sessionHash(slug+"-batch"), []runtimesecurity.Observation{{Method: "GET", Route: "/contacts", Status: 200, TraceRef: strings.Repeat("1", 32), SpanRef: strings.Repeat("2", 16), ObservedAt: time.Now().Add(-time.Second), DataFields: fields, CaptureWarnings: warnings, ValueFingerprints: fingerprints}})
	must(e)
	token, e := valuefingerprint.Create(key, "fixture-key", slug, value)
	must(e)
	encoded, _ := json.Marshal(token)
	handler := New(repo, "https://example.invalid", true)
	call := func(raw, origin, cookie string, status int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("POST", "/api/v1/workspaces/"+slug+"/investigations/value-search", strings.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: "__Host-scarlet-session", Value: cookie})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("HTTP status=%d expected=%d body=%s", w.Code, status, w.Body.String())
		}
		if strings.Contains(w.Body.String(), value) {
			t.Fatal("raw value returned")
		}
		return w
	}
	response := call(string(encoded), "https://example.invalid", session, 200)
	var result store.ValueSearchResult
	must(json.Unmarshal(response.Body.Bytes(), &result))
	if len(result.Items) != 0 || len(result.RuntimeItems) != 1 || result.RuntimeItems[0].SourceID != source.ID || result.RuntimeItems[0].Locator != "email" {
		t.Fatalf("runtime provenance missing: %+v", result)
	}

	block, e := aes.NewCipher([]byte(strings.Repeat("x", 32)))
	must(e)
	gcm, e := cipher.NewGCMWithRandomNonce(block)
	must(e)
	recorder := cryptoevidence.Recorder{KeyHex: key, KeyID: "fixture-key", Scope: slug}
	_, cryptoRecord, e := recorder.Transform(strings.Repeat("1", 32), strings.Repeat("2", 16), "encrypt", "AES-256-GCM", []byte(value), func(input []byte) ([]byte, error) { return gcm.Seal(nil, nil, input, nil), nil })
	must(e)
	cryptoBody, _ := json.Marshal(store.CryptoTransformInput{Kind: "gateway", Records: []cryptoevidence.Record{cryptoRecord}})
	post := httptest.NewRequest("POST", CryptoTransformPath, strings.NewReader(string(cryptoBody)))
	post.Header.Set("Authorization", "Bearer "+runtimeSecret)
	post.Header.Set("Content-Type", "application/json")
	receipt := httptest.NewRecorder()
	handler.ServeHTTP(receipt, post)
	if receipt.Code != 200 {
		t.Fatalf("crypto source HTTP=%d body=%s", receipt.Code, receipt.Body.String())
	}
	unauthorized := httptest.NewRequest("POST", CryptoTransformPath, strings.NewReader(string(cryptoBody)))
	unauthorized.Header.Set("Content-Type", "application/json")
	denied := httptest.NewRecorder()
	handler.ServeHTTP(denied, unauthorized)
	if denied.Code != 401 {
		t.Fatal("crypto evidence accepted without source bearer")
	}
	rawPayload := httptest.NewRequest("POST", CryptoTransformPath, strings.NewReader(`{"kind":"gateway","records":[],"plaintext":"`+value+`"}`))
	rawPayload.Header.Set("Content-Type", "application/json")
	rawPayload.Header.Set("Authorization", "Bearer "+runtimeSecret)
	rejected := httptest.NewRecorder()
	handler.ServeHTTP(rejected, rawPayload)
	if rejected.Code != 400 || strings.Contains(rejected.Body.String(), value) {
		t.Fatal("crypto endpoint accepted or echoed raw payload field")
	}
	related := call(string(encoded), "https://example.invalid", session, 200)
	must(json.Unmarshal(related.Body.Bytes(), &result))
	if len(result.Relations) != 1 || result.Relations[0].TraceRef != cryptoRecord.TraceRef {
		t.Fatal("source-posted crypto relation missing")
	}
	call(string(encoded), "", session, 403)
	call(string(encoded), "https://example.invalid", "", 401)
	call(`{"keyId":"fixture-key","scope":"`+slug+`","version":"utf8_exact_v1","digest":"`+token.Digest+`","rawValue":"`+value+`"}`, "https://example.invalid", session, 400)
	call(`{"keyId":"fixture-key","scope":"`+slug+`","version":"utf8_exact_v1","digest":"bad"}`, "https://example.invalid", session, 400)
}
