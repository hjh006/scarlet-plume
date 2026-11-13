package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"github.com/hjh006/scarlet-plume/backend/internal/runtimesecurity"
	"github.com/hjh006/scarlet-plume/backend/internal/valuefingerprint"
	"github.com/hjh006/scarlet-plume/backend/pkg/cryptoevidence"
	"strings"
	"testing"
	"time"
)

func TestActualCryptoTransformBoundToSourceAuditAndValueInvestigation(t *testing.T) {
	f := assessmentSourceTestStore(t)
	ctx := t.Context()
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	tokenHash := discoveryDigest(f.ws.Slug)
	source, e := f.repo.CreateRuntimeSource(ctx, f.ws.ID, f.user.ID, "Authorized crypto instrumentation", "Crypto application", "gateway", tokenHash)
	must(e)
	aesKey := make([]byte, 32)
	_, e = rand.Read(aesKey)
	must(e)
	defer clear(aesKey)
	block, e := aes.NewCipher(aesKey)
	must(e)
	gcm, e := cipher.NewGCMWithRandomNonce(block)
	must(e)
	recorder := cryptoevidence.Recorder{KeyHex: strings.Repeat("71", 32), KeyID: "local-transform-key", Scope: f.ws.Slug}
	plain := []byte("actual.synthetic@example.invalid")
	encrypted, enc, e := recorder.Transform(strings.Repeat("a", 32), strings.Repeat("b", 16), "encrypt", "AES-256-GCM", plain, func(input []byte) ([]byte, error) { return gcm.Seal(nil, nil, input, nil), nil })
	must(e)
	_, dec, e := recorder.Transform(enc.TraceRef, enc.SpanRef, "decrypt", "AES-256-GCM", encrypted, func(input []byte) ([]byte, error) { return gcm.Open(nil, nil, input, nil) })
	must(e)
	_, e = f.repo.RecordCryptoTransforms(ctx, tokenHash, CryptoTransformInput{Kind: "gateway", Records: []cryptoevidence.Record{enc}})
	if e != ErrEvidenceRequired {
		t.Fatalf("unbound transform accepted: %v", e)
	}
	_, e = f.repo.DeliverRuntimeBatch(ctx, "gateway", tokenHash, discoveryDigest(f.ws.Slug+"-audit"), []runtimesecurity.Observation{{Method: "GET", Route: "/contacts", Status: 200, ObservedAt: time.Now().Add(-time.Second), TraceRef: enc.TraceRef, SpanRef: enc.SpanRef, DataFields: []runtimesecurity.DataField{{Name: "email", Location: "response", Category: "email", Method: "format", Count: 1, Observed: 1}}}})
	must(e)
	count, e := f.repo.RecordCryptoTransforms(ctx, tokenHash, CryptoTransformInput{Kind: "gateway", Records: []cryptoevidence.Record{enc, dec}})
	must(e)
	otherSourceHash := discoveryDigest(f.ws.Slug + "-other-source")
	_, e = f.repo.CreateRuntimeSource(ctx, f.ws.ID, f.user.ID, "Other source without this audit", "Other application", "gateway", otherSourceHash)
	must(e)
	_, e = f.repo.RecordCryptoTransforms(ctx, otherSourceHash, CryptoTransformInput{Kind: "gateway", Records: []cryptoevidence.Record{enc}})
	if e != ErrEvidenceRequired {
		t.Fatal("another source borrowed the matching trace/span audit")
	}
	if count != 2 {
		t.Fatal("two transform events not persisted")
	}
	_, e = f.repo.RecordCryptoTransforms(ctx, tokenHash, CryptoTransformInput{Kind: "gateway", Records: []cryptoevidence.Record{enc, dec}})
	must(e)
	changed := enc
	changed.Algorithm = "Different-declaration"
	_, e = f.repo.RecordCryptoTransforms(ctx, tokenHash, CryptoTransformInput{Kind: "gateway", Records: []cryptoevidence.Record{changed}})
	if e != ErrConflict {
		t.Fatal("immutable transform replay changed")
	}
	query, e := valuefingerprint.Create(recorder.KeyHex, recorder.KeyID, recorder.Scope, string(plain))
	must(e)
	result, e := f.repo.SearchValueFingerprints(ctx, f.ws.ID, f.user.ID, ValueSearchInput{Token: query})
	must(e)
	if len(result.Relations) != 2 || result.Relations[0].SourceID != source.ID || result.Relations[0].EvidenceType != "customer_instrumented_transform" || !result.Relations[0].Current {
		t.Fatalf("source-bound relations missing: %+v", result)
	}
	raw, _ := json.Marshal(result.Relations)
	if strings.Contains(string(raw), string(plain)) || strings.Contains(string(raw), recorder.KeyHex) {
		t.Fatal("raw value or local key persisted")
	}
	other := assessmentSourceTestStore(t)
	none, e := other.repo.SearchCryptoTransformRelations(ctx, other.ws.ID, other.user.ID, query)
	must(e)
	if len(none) != 0 {
		t.Fatal("crypto evidence crossed workspace")
	}
	must(f.repo.RevokeRuntimeSource(ctx, f.ws.ID, f.user.ID, source.ID))
	historical, e := f.repo.SearchCryptoTransformRelations(ctx, f.ws.ID, f.user.ID, query)
	must(e)
	if len(historical) != 2 || historical[0].Current {
		t.Fatal("revoked source retained current claim")
	}
}
