// Package endpointnative provides explicit native operations and separate
// read-only observers. Observers never enforce a policy or transmit content.
// Raw observation fields are process-local inputs for
// the caller's bounded inspection and anonymous evidence projection.
package endpointnative

import (
	"errors"
	"sort"
	"unicode/utf16"
)

var ErrUnsupported = errors.New("native endpoint observation unsupported on this platform")

// Observation must not be serialized directly into an event or log. Complete
// refers only to the captured textual representation, never all device I/O.
// Non-text clipboard formats, queue metadata and volume events are incomplete.
type Observation struct {
	Channel   string
	Kind      string
	RawTarget string
	Sequence  string
	Text      []byte
	Complete  bool
}

const maxClipboardUnits = 16384

// clipboardText only accepts terminated, well-formed UTF-16 within its budget.
// A bounded prefix is useful for positive detection but cannot prove absence.
func clipboardText(units []uint16) ([]byte, bool) {
	limit := min(len(units), maxClipboardUnits)
	n, terminated := 0, false
	for n < limit {
		if units[n] == 0 {
			terminated = true
			break
		}
		n++
	}
	valid := true
	for i := 0; i < n; i++ {
		u := units[i]
		if u >= 0xd800 && u <= 0xdbff {
			if i+1 == n || units[i+1] < 0xdc00 || units[i+1] > 0xdfff {
				valid = false
			} else {
				i++
			}
		} else if u >= 0xdc00 && u <= 0xdfff {
			valid = false
		}
	}
	return []byte(string(utf16.Decode(units[:n]))), terminated && valid
}

func changedVolumes(previous, current map[string]bool) []string {
	var changes []string
	for root := range previous {
		if !current[root] {
			changes = append(changes, root)
		}
	}
	for root := range current {
		if !previous[root] {
			changes = append(changes, root)
		}
	}
	sort.Strings(changes)
	return changes
}
