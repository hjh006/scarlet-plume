package httpapi

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"

	"golang.org/x/crypto/argon2"
)

const passwordScheme = "argon2id-v1"

func derivePassword(password string, salt []byte) []byte {
	return argon2.IDKey([]byte(password), salt, 3, 64*1024, 1, 32)
}

func HashPassword(password string) (string, error) {
	if len(password) < 12 || len(password) > 128 {
		return "", errors.New("password must contain 12–128 UTF-8 bytes")
	}
	var salt [16]byte
	if _, err := rand.Read(salt[:]); err != nil {
		return "", err
	}
	hash := derivePassword(password, salt[:])
	encoding := base64.RawStdEncoding
	return passwordScheme + "$" + encoding.EncodeToString(salt[:]) + "$" + encoding.EncodeToString(hash), nil
}

func verifyPassword(password, encoded string) bool {
	var salt [16]byte
	var expected [32]byte
	valid := false
	if parts := strings.Split(encoded, "$"); len(parts) == 3 && parts[0] == passwordScheme &&
		len(parts[1]) == base64.RawStdEncoding.EncodedLen(len(salt)) &&
		len(parts[2]) == base64.RawStdEncoding.EncodedLen(len(expected)) {
		n, saltErr := base64.RawStdEncoding.Decode(salt[:], []byte(parts[1]))
		m, hashErr := base64.RawStdEncoding.Decode(expected[:], []byte(parts[2]))
		valid = saltErr == nil && hashErr == nil && n == len(salt) && m == len(expected)
	}
	actual := derivePassword(password, salt[:])
	return subtle.ConstantTimeCompare(actual, expected[:]) == 1 && valid
}

func sessionToken() (string, error) {
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(random[:]), nil
}

func sessionHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
