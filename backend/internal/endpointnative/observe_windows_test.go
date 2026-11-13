//go:build windows

package endpointnative

import (
	"testing"
	"unsafe"
)

// These tests never register a window, read a real clipboard, enumerate a
// printer or perform a device operation. Native execution needs an authorized
// Windows acceptance session with synthetic input, separate from unit tests.
func TestNativeBufferStringsCannotEscapeTheirBuffer(t *testing.T) {
	buf := []uint16{'P', 'r', 'i', 'n', 't', 'e', 'r', 0}
	raw := unsafe.Slice((*byte)(unsafe.Pointer(&buf[0])), len(buf)*2)
	base := uintptr(unsafe.Pointer(&buf[0]))
	if value, ok := nativeUTF16String(raw, base, 1024); !ok || value != "Printer" {
		t.Fatal("bounded printer name unavailable")
	}
	for _, pointer := range []uintptr{0, base - 2, base + 1, base + uintptr(len(raw))} {
		if _, ok := nativeUTF16String(raw, pointer, 1024); ok {
			t.Fatal("invalid native string pointer accepted")
		}
	}
	if _, ok := nativeUTF16String(raw, base, 3); ok {
		t.Fatal("unterminated bounded native string accepted")
	}
}

func TestWindowsNativeStructureABI(t *testing.T) {
	printer, job, message, class := uintptr(12), uintptr(64), uintptr(32), uintptr(40)
	if unsafe.Sizeof(uintptr(0)) == 8 {
		printer, job, message, class = 24, 96, 48, 72
	}
	if unsafe.Sizeof(printerInfo4{}) != printer || unsafe.Sizeof(jobInfo1{}) != job || unsafe.Sizeof(windowMessage{}) != message || unsafe.Sizeof(windowClass{}) != class {
		t.Fatal("Windows native structures do not match the platform ABI")
	}
}

func TestUSBOpenedHandleMustMatchTheAuthorizedVolumeGUID(t *testing.T) {
	const expected = `\\?\Volume{00112233-4455-6677-8899-aabbccddeeff}\`
	if !sameVolumeIdentity(expected, expected+`directory\safe.txt`) {
		t.Fatal("same authorized volume rejected")
	}
	if !sameVolumeIdentity(expected, `\\?\VOLUME{00112233-4455-6677-8899-AABBCCDDEEFF}\safe.txt`) {
		t.Fatal("case-insensitive GUID identity rejected")
	}
	for _, path := range []string{`E:\safe.txt`, `\\?\Volume{ffffffff-4455-6677-8899-aabbccddeeff}\safe.txt`, `\\server\share\safe.txt`, expected} {
		if sameVolumeIdentity(expected, path) {
			t.Fatal("different or missing volume identity accepted")
		}
	}
	for _, root := range []string{"", `E:\`, `\\?\Volume{not-a-guid}\`} {
		if sameVolumeIdentity(root, root+"safe.txt") {
			t.Fatal("malformed expected volume identity accepted")
		}
	}
}
