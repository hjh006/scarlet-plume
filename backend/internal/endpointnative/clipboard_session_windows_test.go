//go:build windows

package endpointnative

import (
	"testing"
	"unsafe"
)

// No token, clipboard, application or desktop API is invoked by this ABI test.
func TestClipboardTokenStatisticsABI(t *testing.T) {
	if unsafe.Sizeof(clipboardTokenStatistics{}) != 56 || unsafe.Offsetof(clipboardTokenStatistics{}.AuthenticationID) != 8 || unsafe.Offsetof(clipboardTokenStatistics{}.TokenType) != 24 {
		t.Fatal("Windows token statistics ABI mismatch")
	}
}
