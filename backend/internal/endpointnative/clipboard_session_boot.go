package endpointnative

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
)

var errClipboardGenerationMissing = errors.New("clipboard volatile generation absent")

type clipboardGenerationStore interface {
	load() ([]byte, error)
	saveNew([]byte) error
	withLock(func() error) error
}

// Existing generations are read without taking an initialization lock. When
// called with allowInit=false (clipboard already open), a missing generation
// fails closed rather than creating keys or waiting for another initializer.
func clipboardBootGeneration(allowInit bool, store clipboardGenerationStore) (string, error) {
	read := func() (string, error) {
		raw, err := store.load()
		defer clear(raw)
		if err != nil {
			return "", err
		}
		if len(raw) != 32 {
			return "", ErrClipboardSession
		}
		return hex.EncodeToString(raw), nil
	}
	generation, err := read()
	if err == nil {
		return generation, nil
	}
	if !allowInit || !errors.Is(err, errClipboardGenerationMissing) {
		return "", ErrClipboardSession
	}
	err = store.withLock(func() error {
		var err error
		generation, err = read()
		if err == nil {
			return nil
		}
		if !errors.Is(err, errClipboardGenerationMissing) {
			return ErrClipboardSession
		}
		var nonce [32]byte
		defer clear(nonce[:])
		if _, err := rand.Read(nonce[:]); err != nil {
			return ErrClipboardSession
		}
		if store.saveNew(nonce[:]) != nil {
			return ErrClipboardSession
		}
		verified, err := store.load()
		defer clear(verified)
		if err != nil || !bytes.Equal(verified, nonce[:]) {
			return ErrClipboardSession
		}
		generation = hex.EncodeToString(nonce[:])
		return nil
	})
	if err != nil {
		return "", ErrClipboardSession
	}
	return generation, nil
}
