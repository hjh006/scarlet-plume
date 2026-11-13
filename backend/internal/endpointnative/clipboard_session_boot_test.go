package endpointnative

import (
	"errors"
	"sync"
	"testing"
)

type fakeClipboardGenerationHive struct {
	mu            sync.Mutex
	initMu        sync.Mutex
	value         []byte
	loadErr       error
	writes, locks int
}

func (h *fakeClipboardGenerationHive) load() ([]byte, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.loadErr != nil {
		return nil, h.loadErr
	}
	if h.value == nil {
		return nil, errClipboardGenerationMissing
	}
	return append([]byte(nil), h.value...), nil
}
func (h *fakeClipboardGenerationHive) saveNew(value []byte) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.value != nil {
		return ErrClipboardSession
	}
	h.writes++
	h.value = append([]byte(nil), value...)
	return nil
}
func (h *fakeClipboardGenerationHive) withLock(fn func() error) error {
	h.initMu.Lock()
	defer h.initMu.Unlock()
	h.mu.Lock()
	h.locks++
	h.mu.Unlock()
	return fn()
}

func TestClipboardVolatileGenerationStableAcrossHelpersButNotHiveReload(t *testing.T) {
	h := &fakeClipboardGenerationHive{}
	first, err := clipboardBootGeneration(true, h)
	if err != nil || len(first) != 64 {
		t.Fatal("new volatile generation failed")
	}
	again, err := clipboardBootGeneration(true, h)
	if err != nil || again != first || h.writes != 1 {
		t.Fatal("helper restart changed the same logon's generation")
	}
	f := fakeSession()
	f.id.BootGeneration = first
	old := f.id.LocalKey()
	event := captureClipboardSession(t.Context(), f, 100, f.id)
	defer clear(event.Text)
	// Simulate normal hive unload/reboot: volatile entries are gone even if
	// Windows reuses the same SID, AuthenticationId and numeric session ID.
	h.mu.Lock()
	h.value = nil
	h.mu.Unlock()
	next, err := clipboardBootGeneration(true, h)
	if err != nil || next == first {
		t.Fatal("hive reload revived the previous boot generation")
	}
	f.id.BootGeneration = next
	if f.id.LocalKey() == old {
		t.Fatal("old approved session reference can survive generation change")
	}
	r, _ := clearClipboardSession(t.Context(), event.Match)
	if r.Attempted || f.empties != 0 || r.Status != "session_unavailable" {
		t.Fatal("pre-reboot capture ticket cleared a new-generation session")
	}
}

func TestClipboardVolatileGenerationInitializationIsSerialized(t *testing.T) {
	h := &fakeClipboardGenerationHive{}
	results := make(chan string, 24)
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			g, err := clipboardBootGeneration(true, h)
			if err != nil {
				results <- ""
			} else {
				results <- g
			}
		}()
	}
	wg.Wait()
	close(results)
	var first string
	for g := range results {
		if g == "" {
			t.Fatal("concurrent generation initialization failed")
		}
		if first == "" {
			first = g
		} else if first != g {
			t.Fatal("parallel helpers observed different boot generations")
		}
	}
	if h.writes != 1 {
		t.Fatal("concurrent helpers overwrote the existing nonce")
	}
}

func TestClipboardVolatileGenerationNeverInitializesWhileClipboardIsLocked(t *testing.T) {
	h := &fakeClipboardGenerationHive{}
	if _, err := clipboardBootGeneration(false, h); !errors.Is(err, ErrClipboardSession) || h.writes != 0 || h.locks != 0 {
		t.Fatal("locked clipboard identity path initialized or waited on metadata")
	}
	for _, value := range [][]byte{{1}, make([]byte, 33)} {
		h := &fakeClipboardGenerationHive{value: value}
		if _, err := clipboardBootGeneration(true, h); !errors.Is(err, ErrClipboardSession) || h.writes != 0 {
			t.Fatal("unknown existing generation was overwritten")
		}
	}
	h = &fakeClipboardGenerationHive{loadErr: ErrClipboardSession}
	if _, err := clipboardBootGeneration(true, h); !errors.Is(err, ErrClipboardSession) || h.writes != 0 || h.locks != 0 {
		t.Fatal("untrusted ownership/ACL was repaired or bypassed")
	}
}
