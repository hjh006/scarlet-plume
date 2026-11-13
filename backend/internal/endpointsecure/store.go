// Package endpointsecure stores machine-private configuration and native
// rollback snapshots. It never downgrades to plaintext on other platforms.
package endpointsecure

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
)

var ErrUnavailable = errors.New("protected Windows endpoint store unavailable")

type Store struct {
	root *os.Root
	lock *os.File
}

func Open(path string) (*Store, error) {
	if !filepath.IsAbs(path) {
		return nil, ErrUnavailable
	}
	if err := secureDirectory(path); err != nil {
		return nil, ErrUnavailable
	}
	r, err := os.OpenRoot(path)
	if err != nil {
		return nil, ErrUnavailable
	}
	lock, err := acquireStoreLock(r)
	if err != nil {
		r.Close()
		return nil, ErrUnavailable
	}
	return &Store{root: r, lock: lock}, nil
}
func (s *Store) Close() error {
	rootErr := s.root.Close()
	lockErr := s.lock.Close()
	if rootErr != nil {
		return rootErr
	}
	return lockErr
}
func validName(name string) bool {
	return filepath.IsLocal(name) && filepath.Base(name) == name && len(name) <= 100 && name != "."
}
func (s *Store) Read(name string) ([]byte, error) {
	if !validName(name) {
		return nil, ErrUnavailable
	}
	f, err := s.root.Open(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, os.ErrNotExist
	}
	if err != nil {
		return nil, ErrUnavailable
	}
	defer f.Close()
	if secureExistingFile(f) != nil {
		return nil, ErrUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(f, 4<<20+1))
	if err != nil || len(raw) > 4<<20 {
		return nil, ErrUnavailable
	}
	return unseal(raw)
}
func (s *Store) Write(name string, raw []byte) error {
	if !validName(name) || len(raw) > 2<<20 {
		return ErrUnavailable
	}
	protected, err := seal(raw)
	if err != nil {
		return ErrUnavailable
	}
	defer clear(protected)
	// A private root with SYSTEM/Admin DACL keeps the replacement and snapshots
	// inaccessible to ordinary users. Rename occurs only after a durable flush.
	nonce := make([]byte, 16)
	if _, err = rand.Read(nonce); err != nil {
		return ErrUnavailable
	}
	tmp := hex.EncodeToString(nonce) + ".tmp"
	f, err := createPrivateFile(s.root, tmp)
	if err != nil {
		return ErrUnavailable
	}
	if secureCreatedFile(f, filepath.Join(s.root.Name(), tmp)) != nil {
		_ = f.Close()
		_ = s.root.Remove(tmp)
		return ErrUnavailable
	}
	n, err := f.Write(protected)
	if err == nil && n == len(protected) {
		err = f.Sync()
	} else {
		err = ErrUnavailable
	}
	closed := f.Close()
	if err != nil || closed != nil {
		return ErrUnavailable
	}
	if s.root.Rename(tmp, name) != nil {
		return ErrUnavailable
	}
	return nil
}
func (s *Store) Names() ([]string, error) {
	f, err := s.root.Open(".")
	if err != nil {
		return nil, ErrUnavailable
	}
	defer f.Close()
	entries, err := f.ReadDir(1001)
	if err != nil && err != io.EOF || len(entries) > 1000 {
		return nil, ErrUnavailable
	}
	names := []string{}
	for _, e := range entries {
		if !e.IsDir() && validName(e.Name()) && filepath.Ext(e.Name()) == ".bin" {
			names = append(names, e.Name())
		}
	}
	return names, nil
}
