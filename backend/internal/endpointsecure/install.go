package endpointsecure

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
)

const maxExecutableBytes = 256 << 20

// InstallExecutable pins the bytes copied from one opened source handle to a
// separately approved SHA256. A user-writable source pathname or local manifest
// is never itself the trust authority. SCM registration must happen afterwards.
func (s *Store) InstallExecutable(sourcePath, expectedSHA256 string) (string, error) {
	if !validSHA256(expectedSHA256) {
		return "", ErrUnavailable
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return "", ErrUnavailable
	}
	defer source.Close()
	stat, err := source.Stat()
	if err != nil || !stat.Mode().IsRegular() || stat.Size() < 1 || stat.Size() > maxExecutableBytes {
		return "", ErrUnavailable
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", ErrUnavailable
	}
	name := hex.EncodeToString(nonce[:]) + ".tmp"
	target, err := createPrivateFile(s.root, name)
	if err != nil {
		return "", ErrUnavailable
	}
	committed := false
	defer func() {
		target.Close()
		if !committed {
			_ = s.root.Remove(name)
		}
	}()
	if err := copyVerifiedExecutable(target, source, expectedSHA256); err != nil {
		return "", err
	}
	if secureExistingFile(target) != nil {
		return "", ErrUnavailable
	}
	if target.Close() != nil {
		return "", ErrUnavailable
	}
	path := filepath.Join(s.root.Name(), "scarlet-endpoint-agent.exe")
	if commitExecutable(filepath.Join(s.root.Name(), name), path) != nil {
		return "", ErrUnavailable
	}
	committed = true
	return path, nil
}

func validSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func copyVerifiedExecutable(target *os.File, source io.Reader, expected string) error {
	if !validSHA256(expected) {
		return ErrUnavailable
	}
	n, err := io.Copy(target, io.LimitReader(source, maxExecutableBytes+1))
	if err != nil || n < 1 || n > maxExecutableBytes || target.Sync() != nil {
		return ErrUnavailable
	}
	if _, err := target.Seek(0, io.SeekStart); err != nil {
		return ErrUnavailable
	}
	h := sha256.New()
	verified, err := io.Copy(h, io.LimitReader(target, maxExecutableBytes+1))
	if err != nil || verified != n || hex.EncodeToString(h.Sum(nil)) != expected {
		return ErrUnavailable
	}
	return nil
}
