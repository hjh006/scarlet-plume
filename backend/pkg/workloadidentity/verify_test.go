package workloadidentity

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func signedFixture(t *testing.T, algorithm string) (Policy, func(string, string) string) {
	t.Helper()
	encode := base64.RawURLEncoding.EncodeToString
	jwk := map[string]any{"kid": "signing-key-1", "use": "sig", "alg": algorithm, "key_ops": []string{"verify"}}
	var sign func([]byte) []byte
	switch algorithm {
	case "RS256":
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		jwk["kty"], jwk["n"], jwk["e"] = "RSA", encode(key.N.Bytes()), "AQAB"
		sign = func(raw []byte) []byte {
			h := sha256.Sum256(raw)
			s, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, h[:])
			if err != nil {
				t.Fatal(err)
			}
			return s
		}
	case "ES256":
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		jwk["kty"], jwk["crv"], jwk["x"], jwk["y"] = "EC", "P-256", encode(key.X.FillBytes(make([]byte, 32))), encode(key.Y.FillBytes(make([]byte, 32)))
		sign = func(raw []byte) []byte {
			h := sha256.Sum256(raw)
			r, s, err := ecdsa.Sign(rand.Reader, key, h[:])
			if err != nil {
				t.Fatal(err)
			}
			return append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...)
		}
	case "EdDSA":
		pub, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		jwk["kty"], jwk["crv"], jwk["x"] = "OKP", "Ed25519", encode(pub)
		sign = func(raw []byte) []byte { return ed25519.Sign(key, raw) }
	default:
		t.Fatal("unknown fixture algorithm")
	}
	keys, _ := json.Marshal(map[string]any{"keys": []any{jwk}})
	p := Policy{Issuer: "https://issuer.example.invalid/workloads", Audience: "scarlet-workload:synthetic", Subject: "spiffe://example.invalid/agent/support", JWKS: keys, MaxTTLSeconds: 60}
	return p, func(header, body string) string {
		raw := encode([]byte(header)) + "." + encode([]byte(body))
		return raw + "." + encode(sign([]byte(raw)))
	}
}

func claimsFixture(p Policy, now time.Time) map[string]any {
	return map[string]any{"iss": p.Issuer, "aud": p.Audience, "sub": p.Subject, "iat": now.Unix(), "exp": now.Unix() + 120, "jti": "synthetic-assertion-0001", "roles": []string{"untrusted-admin"}, "applicationId": "ignored"}
}

func rawJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestWorkloadAssertionsVerifyRealRS256ES256Ed25519Signatures(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for _, alg := range []string{"RS256", "ES256", "EdDSA"} {
		t.Run(alg, func(t *testing.T) {
			p, sign := signedFixture(t, alg)
			v, err := New(p)
			if err != nil {
				t.Fatal(err)
			}
			header := rawJSON(map[string]any{"alg": alg, "kid": "signing-key-1", "typ": "JWT"})
			token := sign(header, rawJSON(claimsFixture(p, now)))
			proof, err := v.Verify(token, now)
			if err != nil || proof.Subject != p.Subject || proof.Algorithm != alg || len(proof.JTIHash) != 64 || !proof.ExpiresAt.Equal(now.Add(time.Minute)) {
				t.Fatal("signed workload rejected", proof, err)
			}
			parts := strings.Split(token, ".")
			parts[1] = base64.RawURLEncoding.EncodeToString([]byte(strings.Replace(rawJSON(claimsFixture(p, now)), p.Subject, "forged-subject", 1)))
			if _, err := v.Verify(strings.Join(parts, "."), now); err == nil {
				t.Fatal("modified signed body accepted")
			}
		})
	}
}

func TestWorkloadAssertionsRejectConfusionReplayInputsAndInvalidClaims(t *testing.T) {
	p, sign := signedFixture(t, "EdDSA")
	v, _ := New(p)
	now := time.Now().UTC().Truncate(time.Second)
	header := rawJSON(map[string]string{"alg": "EdDSA", "kid": "signing-key-1", "typ": "JWT"})
	for _, mutate := range []func(map[string]any){
		func(c map[string]any) { c["iss"] = "https://untrusted.example.invalid" },
		func(c map[string]any) { c["sub"] = "different-agent" },
		func(c map[string]any) { c["aud"] = "different-audience" },
		func(c map[string]any) { c["aud"] = []string{p.Audience, "another-service"} },
		func(c map[string]any) { delete(c, "iat") },
		func(c map[string]any) { c["iat"] = now.Unix() + 31 },
		func(c map[string]any) { c["iat"] = "123" },
		func(c map[string]any) { c["exp"] = now.Unix() },
		func(c map[string]any) { c["exp"] = now.Unix() + 301 },
		func(c map[string]any) { c["nbf"] = now.Unix() + 31 },
		func(c map[string]any) { delete(c, "jti") },
		func(c map[string]any) { c["jti"] = strings.Repeat("x", 161) },
		func(c map[string]any) { c["ISS"] = p.Issuer },
	} {
		c := claimsFixture(p, now)
		mutate(c)
		if _, err := v.Verify(sign(header, rawJSON(c)), now); err == nil {
			t.Fatal("invalid workload claims accepted")
		}
	}
	for _, h := range []string{
		`{"alg":"none","kid":"signing-key-1","typ":"JWT"}`,
		`{"alg":"HS256","kid":"signing-key-1","typ":"JWT"}`,
		`{"alg":"EdDSA","kid":"unknown","typ":"JWT"}`,
		`{"alg":"EdDSA","kid":"signing-key-1","typ":"JWT","jku":"https://attacker.invalid/key"}`,
		`{"alg":"EdDSA","ALG":"EdDSA","kid":"signing-key-1","typ":"JWT"}`,
		`{"alg":"EdDSA","kid":"signing-key-1","typ":"JWT","crit":["b64"],"b64":false}`,
		`{"alg":"EdDSA","kid":"signing-key-1"}`,
	} {
		if _, err := v.Verify(sign(h, rawJSON(claimsFixture(p, now))), now); err == nil {
			t.Fatal("ambiguous or remote-key header accepted")
		}
	}
	duplicate := strings.TrimSuffix(rawJSON(claimsFixture(p, now)), "}") + `,"sub":"` + p.Subject + `"}`
	if _, err := v.Verify(sign(header, duplicate), now); err == nil {
		t.Fatal("duplicate signed claim accepted")
	}
	if _, err := v.Verify(sign(header, rawJSON(claimsFixture(p, now))), now.Add(2*time.Minute)); err == nil {
		t.Fatal("expired assertion accepted")
	}
	c := claimsFixture(p, now)
	c["aud"] = []string{p.Audience}
	if _, err := v.Verify(sign(header, rawJSON(c)), now); err != nil {
		t.Fatal("single exact audience array rejected")
	}
}

func TestWorkloadPublicKeyImportRejectsPrivateKeysAlgorithmsAndBounds(t *testing.T) {
	p, _ := signedFixture(t, "EdDSA")
	for _, mutate := range []func(*Policy){
		func(p *Policy) { p.Issuer = "http://issuer.example.invalid" },
		func(p *Policy) { p.Issuer += "?query" },
		func(p *Policy) { p.MaxTTLSeconds = 301 },
		func(p *Policy) { p.Subject = " " },
		func(p *Policy) { p.JWKS = json.RawMessage(`{"keys":[]}`) },
		func(p *Policy) { p.JWKS = json.RawMessage(strings.Repeat(" ", 16385)) },
		func(p *Policy) {
			p.JWKS = json.RawMessage(strings.Replace(string(p.JWKS), `"use":"sig"`, `"use":"enc"`, 1))
		},
		func(p *Policy) {
			p.JWKS = json.RawMessage(strings.Replace(string(p.JWKS), `"alg":"EdDSA"`, `"alg":"HS256"`, 1))
		},
		func(p *Policy) { p.JWKS = json.RawMessage(strings.Replace(string(p.JWKS), `"verify"`, `"sign"`, 1)) },
		func(p *Policy) { p.JWKS = json.RawMessage(strings.Replace(string(p.JWKS), `["verify"]`, `[]`, 1)) },
		func(p *Policy) { p.JWKS = json.RawMessage(strings.Replace(string(p.JWKS), `["verify"]`, `null`, 1)) },
		func(p *Policy) {
			p.JWKS = json.RawMessage(strings.Replace(string(p.JWKS), `"kty":"OKP"`, `"kty":"OKP","d":"synthetic-private-key"`, 1))
		},
		func(p *Policy) {
			p.JWKS = json.RawMessage(strings.Replace(string(p.JWKS), `"kty":"OKP"`, `"kty":"OKP","x5u":"https://attacker.invalid"`, 1))
		},
	} {
		q := p
		mutate(&q)
		if _, err := New(q); err == nil {
			t.Fatal("unsafe public-key configuration accepted")
		}
	}
	var set map[string][]map[string]any
	json.Unmarshal(p.JWKS, &set)
	set["keys"] = append(set["keys"], set["keys"][0])
	p.JWKS = json.RawMessage(rawJSON(set))
	if _, err := New(p); err == nil {
		t.Fatal("ambiguous key ID accepted")
	}
}
