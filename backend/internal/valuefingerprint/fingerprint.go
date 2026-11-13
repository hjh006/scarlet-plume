// Package valuefingerprint creates source-local, domain-bound equality tokens.
// Tokens remain sensitive correlation identifiers; they are never proof of lineage.
package valuefingerprint

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"unicode/utf8"
)

const Version = "utf8_exact_v1"
const BinaryVersion = "bytes_exact_v1"

var identifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,79}$`)
var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

type Token struct {
	KeyID   string `json:"keyId"`
	Scope   string `json:"scope"`
	Version string `json:"version"`
	Digest  string `json:"digest"`
}
type Evidence struct {
	Token
	Locator  string `json:"locator"`
	Category string `json:"category"`
}

func Valid(t Token) bool {
	return identifier.MatchString(t.KeyID) && identifier.MatchString(t.Scope) && (t.Version == Version || t.Version == BinaryVersion) && digestPattern.MatchString(t.Digest)
}
func Create(keyHex, keyID, scope, value string) (Token, error) {
	if !utf8.ValidString(value) || len(value) > 4096 {
		return Token{}, errors.New("invalid bounded UTF-8 value")
	}
	return create(keyHex, keyID, scope, []byte(value), Version)
}
func CreateBytes(keyHex, keyID, scope string, value []byte) (Token, error) {
	return create(keyHex, keyID, scope, value, BinaryVersion)
}
func create(keyHex, keyID, scope string, value []byte, version string) (Token, error) {
	key, e := hex.DecodeString(keyHex)
	if e != nil || len(key) != 32 || !identifier.MatchString(keyID) || !identifier.MatchString(scope) || len(value) == 0 || len(value) > 8192 {
		return Token{}, errors.New("invalid fingerprint configuration or bounded value")
	}
	defer clear(key)
	h := hmac.New(sha256.New, key)
	h.Write([]byte(version + "\x00" + scope + "\x00"))
	h.Write(value)
	return Token{KeyID: keyID, Scope: scope, Version: version, Digest: hex.EncodeToString(h.Sum(nil))}, nil
}
