package endpointnative

import (
	"strings"
	"testing"
	"unicode/utf16"
)

func TestClipboardTextBoundedRepresentation(t *testing.T) {
	for _, tc := range []struct {
		name     string
		units    []uint16
		text     string
		complete bool
	}{
		{"text", append(utf16.Encode([]rune("synthetic@example.invalid 中文 😀")), 0), "synthetic@example.invalid 中文 😀", true},
		{"empty", []uint16{0}, "", true},
		{"unavailable", nil, "", false},
		{"unterminated", []uint16{'a', 'b'}, "ab", false},
		{"invalid high surrogate", []uint16{0xd800, 0}, "�", false},
		{"invalid low surrogate", []uint16{0xdc00, 0}, "�", false},
		{"string termination", []uint16{'a', 0, 'b', 0}, "a", true},
		{"limit", append(utf16.Encode([]rune(strings.Repeat("界", maxClipboardUnits))), 0), strings.Repeat("界", maxClipboardUnits), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text, complete := clipboardText(tc.units)
			if string(text) != tc.text || complete != tc.complete {
				t.Fatalf("unexpected bounded representation: bytes=%d complete=%v", len(text), complete)
			}
			if len(text) > maxClipboardUnits*3 {
				t.Fatal("UTF-8 text budget exceeded")
			}
		})
	}
}

func TestVolumeChangesDoNotImplyFileWrites(t *testing.T) {
	previous := map[string]bool{"E:\\": true, "F:\\": true}
	current := map[string]bool{"E:\\": true, "G:\\": true}
	changes := changedVolumes(previous, current)
	if len(changes) != 2 || changes[0] != "F:\\" || changes[1] != "G:\\" {
		t.Fatalf("unexpected changed roots: %v", changes)
	}
	if len(changedVolumes(current, current)) != 0 {
		t.Fatal("stable removable volume generated an event")
	}
	if !previous["F:\\"] || !current["G:\\"] {
		t.Fatal("source snapshots mutated")
	}
}
