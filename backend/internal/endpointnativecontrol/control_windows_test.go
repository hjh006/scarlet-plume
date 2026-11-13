//go:build windows

package endpointnativecontrol

import (
	"testing"
	"unsafe"
)

// Pure tests only. These do not open registry keys, spooler queues or devices.
func TestFixedWindowsABIAndBoundedNativeStrings(t *testing.T) {
	defaults, info := uintptr(12), uintptr(84)
	if unsafe.Sizeof(uintptr(0)) == 8 {
		defaults, info = 24, 136
	}
	if unsafe.Sizeof(printerDefaults{}) != defaults || unsafe.Sizeof(printerInfo2{}) != info {
		t.Fatal("Windows printer ABI layout mismatch")
	}
	units := []uint16{'Q', 'u', 'e', 'u', 'e', 0}
	buf := unsafe.Slice((*byte)(unsafe.Pointer(&units[0])), len(units)*2)
	p := uintptr(unsafe.Pointer(&units[0]))
	if value, ok := boundedNativeString(buf, p); !ok || value != "Queue" {
		t.Fatal("bounded printer metadata unavailable")
	}
	for _, bad := range []uintptr{p - 2, p + 1, p + uintptr(len(buf))} {
		if _, ok := boundedNativeString(buf, bad); ok {
			t.Fatal("native printer pointer escaped the buffer")
		}
	}
	if _, _, ok := registryTarget("arbitrary_registry_key"); ok {
		t.Fatal("unfixed registry target accepted")
	}
	if policyEdition(0x65) || policyEdition(0) || !policyEdition(0x30) || !policyEdition(0x04) {
		t.Fatal("unsupported policy edition mapping")
	}
}
