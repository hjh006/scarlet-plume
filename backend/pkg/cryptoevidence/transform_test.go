package cryptoevidence

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"github.com/hjh006/scarlet-plume/backend/internal/valuefingerprint"
	"strings"
	"testing"
)

func TestActualAESGCMTransformEvidenceExcludesPlaintextCiphertextAndKeys(t *testing.T) {
	key := make([]byte, 32)
	if _, e := rand.Read(key); e != nil {
		t.Fatal(e)
	}
	defer clear(key)
	block, e := aes.NewCipher(key)
	if e != nil {
		t.Fatal(e)
	}
	gcm, e := cipher.NewGCMWithRandomNonce(block)
	if e != nil {
		t.Fatal(e)
	}
	recorder := Recorder{KeyHex: strings.Repeat("31", 32), KeyID: "client-key-1", Scope: "client-workspace"}
	plain := []byte("actual.synthetic@example.invalid")
	encrypted, enc, e := recorder.Transform(strings.Repeat("1", 32), strings.Repeat("2", 16), "encrypt", "AES-256-GCM", plain, func(input []byte) ([]byte, error) { return gcm.Seal(nil, nil, input, nil), nil })
	if e != nil {
		t.Fatal(e)
	}
	decrypted, dec, e := recorder.Transform(enc.TraceRef, enc.SpanRef, "decrypt", "AES-256-GCM", encrypted, func(input []byte) ([]byte, error) { return gcm.Open(nil, nil, input, nil) })
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(decrypted, plain) || !Valid(enc) || !Valid(dec) || enc.Output != dec.Input || enc.Input != dec.Output {
		t.Fatal("actual encryption/decryption correspondence lost")
	}
	query, e := valuefingerprint.Create(recorder.KeyHex, recorder.KeyID, recorder.Scope, string(plain))
	if e != nil || query != enc.Input {
		t.Fatal("query token and SDK input differ")
	}
	serialized, _ := json.Marshal([]Record{enc, dec})
	if bytes.Contains(serialized, plain) || bytes.Contains(serialized, encrypted) || bytes.Contains(serialized, key) || bytes.Contains(serialized, []byte(recorder.KeyHex)) {
		t.Fatal("source secret or value serialized")
	}
}
