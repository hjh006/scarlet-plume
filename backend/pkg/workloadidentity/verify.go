// Package workloadidentity verifies a deliberately narrow signed workload
// assertion profile. Trust comes from imported public keys, never token URLs.
package workloadidentity

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var ErrInvalid = errors.New("invalid workload trust configuration")
var ErrAssertion = errors.New("workload assertion rejected")
var keyID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,79}$`)

type Policy struct {
	Issuer, Audience, Subject string
	JWKS                      json.RawMessage
	MaxTTLSeconds             int64
}

type Proof struct {
	Issuer, Audience, Subject, JTIHash, KeyID, Algorithm string
	IssuedAt, ExpiresAt                                  time.Time
}

type publicJWK struct {
	Kty    string          `json:"kty"`
	Kid    string          `json:"kid"`
	Use    string          `json:"use"`
	Alg    string          `json:"alg"`
	KeyOps json.RawMessage `json:"key_ops,omitempty"`
	N      string          `json:"n,omitempty"`
	E      string          `json:"e,omitempty"`
	Crv    string          `json:"crv,omitempty"`
	X      string          `json:"x,omitempty"`
	Y      string          `json:"y,omitempty"`
}

type verificationKey struct {
	algorithm string
	key       crypto.PublicKey
}

type Verifier struct {
	policy Policy
	keys   map[string]verificationKey
}

func text(s string, max int) bool {
	return len(s) > 0 && len(s) <= max && utf8.ValidString(s) && strings.TrimSpace(s) == s && !strings.ContainsFunc(s, unicode.IsControl)
}

func decode64(s string, max int) ([]byte, error) {
	if s == "" || len(s) > (max*4+2)/3 {
		return nil, ErrInvalid
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(b) > max || base64.RawURLEncoding.EncodeToString(b) != s {
		return nil, ErrInvalid
	}
	return b, nil
}

func New(p Policy) (*Verifier, error) {
	u, err := url.Parse(p.Issuer)
	if err != nil || !text(p.Issuer, 512) || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || strings.Contains(p.Issuer, "#") || u.Fragment != "" || u.Opaque != "" || !text(p.Audience, 512) || !text(p.Subject, 255) || p.MaxTTLSeconds < 1 || p.MaxTTLSeconds > 300 || len(p.JWKS) > 16384 {
		return nil, ErrInvalid
	}
	var set struct {
		Keys []publicJWK `json:"keys"`
	}
	if decodeJSON(p.JWKS, &set, true) != nil || len(set.Keys) < 1 || len(set.Keys) > 8 {
		return nil, ErrInvalid
	}
	v := &Verifier{policy: p, keys: map[string]verificationKey{}}
	for _, k := range set.Keys {
		if !keyID.MatchString(k.Kid) || k.Use != "sig" {
			return nil, ErrInvalid
		}
		if len(k.KeyOps) > 0 {
			var operations []string
			if json.Unmarshal(k.KeyOps, &operations) != nil || len(operations) != 1 || operations[0] != "verify" {
				return nil, ErrInvalid
			}
		}
		if _, exists := v.keys[k.Kid]; exists {
			return nil, ErrInvalid
		}
		var key crypto.PublicKey
		switch {
		case k.Kty == "RSA" && k.Alg == "RS256" && k.Crv == "" && k.X == "" && k.Y == "":
			n, e1 := decode64(k.N, 512)
			e, e2 := decode64(k.E, 3)
			modulus := new(big.Int).SetBytes(n)
			if e1 != nil || e2 != nil || len(n) < 256 || n[0] == 0 || modulus.BitLen() < 2048 || modulus.BitLen() > 4096 || modulus.Bit(0) != 1 || !bytes.Equal(e, []byte{1, 0, 1}) {
				return nil, ErrInvalid
			}
			key = &rsa.PublicKey{N: modulus, E: 65537}
		case k.Kty == "EC" && k.Alg == "ES256" && k.Crv == "P-256" && k.N == "" && k.E == "":
			x, e1 := decode64(k.X, 32)
			y, e2 := decode64(k.Y, 32)
			px, py := new(big.Int).SetBytes(x), new(big.Int).SetBytes(y)
			if e1 != nil || e2 != nil || len(x) != 32 || len(y) != 32 || !elliptic.P256().IsOnCurve(px, py) {
				return nil, ErrInvalid
			}
			key = &ecdsa.PublicKey{Curve: elliptic.P256(), X: px, Y: py}
		case k.Kty == "OKP" && k.Alg == "EdDSA" && k.Crv == "Ed25519" && k.N == "" && k.E == "" && k.Y == "":
			x, e := decode64(k.X, ed25519.PublicKeySize)
			if e != nil || len(x) != ed25519.PublicKeySize || bytes.Equal(x, make([]byte, ed25519.PublicKeySize)) {
				return nil, ErrInvalid
			}
			key = ed25519.PublicKey(x)
		default:
			return nil, ErrInvalid
		}
		v.keys[k.Kid] = verificationKey{algorithm: k.Alg, key: key}
	}
	return v, nil
}

func (v *Verifier) Verify(assertion string, now time.Time) (Proof, error) {
	var out Proof
	if len(assertion) > 12288 || !utf8.ValidString(assertion) {
		return out, ErrAssertion
	}
	parts := strings.Split(assertion, ".")
	if len(parts) != 3 {
		return out, ErrAssertion
	}
	header, e1 := decode64(parts[0], 1024)
	body, e2 := decode64(parts[1], 8192)
	signature, e3 := decode64(parts[2], 512)
	var h struct {
		Algorithm string `json:"alg"`
		KeyID     string `json:"kid"`
		Type      string `json:"typ"`
	}
	if e1 != nil || e2 != nil || e3 != nil || decodeJSON(header, &h, true) != nil || h.Type != "JWT" && h.Type != "at+jwt" || !keyID.MatchString(h.KeyID) {
		return out, ErrAssertion
	}
	k, ok := v.keys[h.KeyID]
	if !ok || k.algorithm != h.Algorithm {
		return out, ErrAssertion
	}
	signed := []byte(parts[0] + "." + parts[1])
	hash := sha256.Sum256(signed)
	valid := false
	switch key := k.key.(type) {
	case *rsa.PublicKey:
		valid = rsa.VerifyPKCS1v15(key, crypto.SHA256, hash[:], signature) == nil
	case *ecdsa.PublicKey:
		valid = len(signature) == 64 && ecdsa.Verify(key, hash[:], new(big.Int).SetBytes(signature[:32]), new(big.Int).SetBytes(signature[32:]))
	case ed25519.PublicKey:
		valid = ed25519.Verify(key, signed, signature)
	}
	if !valid {
		return out, ErrAssertion
	}
	var c struct {
		Issuer    string          `json:"iss"`
		Subject   string          `json:"sub"`
		Audience  json.RawMessage `json:"aud"`
		IssuedAt  int64           `json:"iat"`
		ExpiresAt int64           `json:"exp"`
		NotBefore *int64          `json:"nbf"`
		JTI       string          `json:"jti"`
	}
	var claims map[string]json.RawMessage
	if json.Unmarshal(body, &claims) != nil {
		return out, ErrAssertion
	}
	for name := range claims {
		lower := strings.ToLower(name)
		if name != lower && (lower == "iss" || lower == "sub" || lower == "aud" || lower == "iat" || lower == "exp" || lower == "nbf" || lower == "jti") {
			return out, ErrAssertion
		}
	}
	// Additional claims may be present, but grant no application, role or ACL.
	if decodeJSON(body, &c, false) != nil || c.Issuer != v.policy.Issuer || c.Subject != v.policy.Subject || !text(c.JTI, 160) || c.IssuedAt < 1 || c.ExpiresAt < 1 || c.IssuedAt >= c.ExpiresAt || c.ExpiresAt-c.IssuedAt > 300 || c.IssuedAt > now.Unix()+30 || c.ExpiresAt <= now.Unix() || c.NotBefore != nil && (*c.NotBefore < 1 || *c.NotBefore > now.Unix()+30 || *c.NotBefore >= c.ExpiresAt) {
		return out, ErrAssertion
	}
	var aud string
	if json.Unmarshal(c.Audience, &aud) != nil {
		var values []string
		if json.Unmarshal(c.Audience, &values) != nil || len(values) != 1 {
			return out, ErrAssertion
		}
		aud = values[0]
	}
	if aud != v.policy.Audience {
		return out, ErrAssertion
	}
	jti := sha256.Sum256([]byte(c.JTI))
	expires := time.Unix(c.ExpiresAt, 0).UTC()
	if cap := now.Add(time.Duration(v.policy.MaxTTLSeconds) * time.Second); cap.Before(expires) {
		expires = cap
	}
	return Proof{Issuer: c.Issuer, Audience: aud, Subject: c.Subject, JTIHash: hex.EncodeToString(jti[:]), KeyID: h.KeyID, Algorithm: h.Algorithm, IssuedAt: time.Unix(c.IssuedAt, 0).UTC(), ExpiresAt: expires}, nil
}

// Reject duplicate members before decoding typed security claims. Unknown claims
// can be ignored only after this bounded structural check removes ambiguity.
func decodeJSON(raw []byte, out any, rejectUnknown bool) error {
	if !utf8.Valid(raw) {
		return ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	nodes := 0
	var value func(int) error
	value = func(depth int) error {
		nodes++
		if depth > 8 || nodes > 1024 {
			return ErrInvalid
		}
		token, err := d.Token()
		if err != nil {
			return err
		}
		if delim, ok := token.(json.Delim); ok {
			if delim != '{' && delim != '[' {
				return ErrInvalid
			}
			seen := map[string]bool{}
			for d.More() {
				if delim == '{' {
					t, err := d.Token()
					name, ok := t.(string)
					if err != nil || !ok || seen[name] || rejectUnknown && name != strings.ToLower(name) {
						return ErrInvalid
					}
					seen[name] = true
				}
				if err := value(depth + 1); err != nil {
					return err
				}
			}
			if _, err = d.Token(); err != nil {
				return err
			}
		}
		return nil
	}
	if err := value(0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return ErrInvalid
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	if rejectUnknown {
		d.DisallowUnknownFields()
	}
	if err := d.Decode(out); err != nil {
		return err
	}
	return nil
}
