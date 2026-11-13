package endpointsecure

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixtureHash(raw []byte) string { h := sha256.Sum256(raw); return hex.EncodeToString(h[:]) }

func TestExecutableRequiresApprovedHashOfTheWrittenBytes(t *testing.T) {
	raw := []byte("synthetic executable image; no native code")
	for _, expected := range []string{"", strings.Repeat("0", 64), strings.ToUpper(fixtureHash(raw)), fixtureHash(raw)} {
		f, err := os.CreateTemp(t.TempDir(), "synthetic-image-")
		if err != nil {
			t.Fatal(err)
		}
		err = copyVerifiedExecutable(f, bytes.NewReader(raw), expected)
		f.Close()
		if expected == fixtureHash(raw) {
			if err != nil {
				t.Fatal("approved exact bytes rejected", err)
			}
		} else if !errors.Is(err, ErrUnavailable) {
			t.Fatal("unapproved executable accepted")
		}
	}
	writeOnly, err := os.OpenFile(filepath.Join(t.TempDir(), "unverifiable-image"), os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer writeOnly.Close()
	if copyVerifiedExecutable(writeOnly, bytes.NewReader(raw), fixtureHash(raw)) == nil {
		t.Fatal("source hashing substituted for actual target readback")
	}
}

func TestExecutableSourceHandleDoesNotFollowAReplacedPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.bin")
	approved := []byte("synthetic approved executable")
	if os.WriteFile(path, approved, 0600) != nil {
		t.Fatal("fixture unavailable")
	}
	source, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if err := os.Rename(path, path+".old"); err != nil {
		t.Skip("platform does not permit replacing this open fixture")
	}
	if os.WriteFile(path, []byte("synthetic replaced executable"), 0600) != nil {
		t.Fatal("fixture replacement unavailable")
	}
	target, err := os.CreateTemp(dir, "private-target-")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	if err := copyVerifiedExecutable(target, source, fixtureHash(approved)); err != nil {
		t.Fatal("opened source handle was not retained")
	}
	changed, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer changed.Close()
	other, err := os.CreateTemp(dir, "other-target-")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if copyVerifiedExecutable(other, changed, fixtureHash(approved)) == nil {
		t.Fatal("replaced source bypassed the approved hash")
	}
}
