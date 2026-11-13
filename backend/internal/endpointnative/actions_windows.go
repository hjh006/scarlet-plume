//go:build windows

package endpointnative

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	actKernel = windows.NewLazySystemDLL("kernel32.dll")
	actUser   = windows.NewLazySystemDLL("user32.dll")
	actGDI    = windows.NewLazySystemDLL("gdi32.dll")
	errNative = errors.New("native endpoint operation unavailable or incomplete")
)

// CopyUSB admits only a currently mounted DRIVE_REMOVABLE volume and never
// overwrites. Fixed USB disks are deliberately unsupported, not guessed.
func CopyUSB(root, destination string, raw []byte) error {
	if !filepath.IsLocal(destination) || len(raw) > 1<<20 {
		return errNative
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return errNative
	}
	p, err := windows.UTF16PtrFromString(abs)
	if err != nil {
		return errNative
	}
	volume := make([]uint16, 32768)
	ok, _, _ := actKernel.NewProc("GetVolumePathNameW").Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&volume[0])), uintptr(len(volume)))
	if ok == 0 {
		return errNative
	}
	kind, _, _ := actKernel.NewProc("GetDriveTypeW").Call(uintptr(unsafe.Pointer(&volume[0])))
	if kind != 2 {
		return errNative
	}
	var identity [50]uint16
	if windows.GetVolumeNameForVolumeMountPoint(&volume[0], &identity[0], uint32(len(identity))) != nil {
		return errNative
	}
	expectedVolume := windows.UTF16ToString(identity[:])
	if windows.GetDriveType(&identity[0]) != 2 {
		return errNative
	}
	dir, err := os.OpenRoot(abs)
	if err != nil {
		return errNative
	}
	defer dir.Close()
	f, err := dir.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errNative
	}
	defer f.Close()
	// Check the opened object as well as the caller's path, before content is
	// written. A directory junction or drive-letter change cannot authorize a
	// write to a fixed/network volume merely by changing a pathname.
	final := make([]uint16, 32768)
	// VOLUME_NAME_GUID avoids authorizing the opened object by a drive letter
	// that could have been remapped since the root was checked.
	npath, pathErr := windows.GetFinalPathNameByHandle(windows.Handle(f.Fd()), &final[0], uint32(len(final)), 1)
	if pathErr != nil || npath == 0 || int(npath) >= len(final) {
		return errNative
	}
	if !sameVolumeIdentity(expectedVolume, windows.UTF16ToString(final[:npath])) || windows.GetDriveType(&identity[0]) != 2 {
		return errNative
	}
	n, err := f.Write(raw)
	if err != nil || n != len(raw) {
		return errNative
	}
	if f.Sync() != nil || f.Close() != nil {
		return errNative
	}
	return nil
}

func sameVolumeIdentity(expected, openedPath string) bool {
	// The canonical GUID root is exactly \\?\Volume{36-character GUID}\.
	if len(expected) != 49 || !strings.EqualFold(expected[:10], `\\?\Volume`) || expected[48] != '\\' {
		return false
	}
	if _, err := windows.GUIDFromString(expected[10:48]); err != nil {
		return false
	}
	return len(openedPath) > len(expected) && strings.EqualFold(openedPath[:len(expected)], expected)
}

func clipboardMemory(format uintptr, value []byte) error {
	h, _, _ := actKernel.NewProc("GlobalAlloc").Call(0x42, uintptr(len(value))) // GMEM_MOVEABLE | ZEROINIT
	if h == 0 {
		return errNative
	}
	owned := true
	defer func() {
		if owned {
			actKernel.NewProc("GlobalFree").Call(h)
		}
	}()
	p, _, _ := actKernel.NewProc("GlobalLock").Call(h)
	if p == 0 {
		return errNative
	}
	var written uintptr
	writeErr := windows.WriteProcessMemory(windows.CurrentProcess(), p, &value[0], uintptr(len(value)), &written)
	actKernel.NewProc("GlobalUnlock").Call(h)
	if writeErr != nil || written != uintptr(len(value)) {
		return errNative
	}
	r, _, _ := actUser.NewProc("SetClipboardData").Call(format, h)
	if r == 0 {
		return errNative
	}
	owned = false
	return nil
}

// WriteClipboard is a prechecked, explicit text write. It neither intercepts
// other applications nor clears content after those applications copied it.
func WriteClipboard(raw []byte) error {
	if len(raw) > 1<<20 || !utf8.Valid(raw) || strings.IndexByte(string(raw), 0) >= 0 {
		return errNative
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	class, _ := windows.UTF16PtrFromString("STATIC")
	hwnd, _, _ := actUser.NewProc("CreateWindowExW").Call(0, uintptr(unsafe.Pointer(class)), 0, 0, 0, 0, 0, 0, ^uintptr(2), 0, 0, 0)
	if hwnd == 0 {
		return errNative
	}
	defer actUser.NewProc("DestroyWindow").Call(hwnd)
	ok, _, _ := actUser.NewProc("OpenClipboard").Call(hwnd)
	if ok == 0 {
		return errNative
	}
	defer actUser.NewProc("CloseClipboard").Call()
	ok, _, _ = actUser.NewProc("EmptyClipboard").Call()
	if ok == 0 {
		return errNative
	}
	// Prevent this mediated text from entering the Windows history/cloud path.
	for _, name := range []string{"CanIncludeInClipboardHistory", "CanUploadToCloudClipboard"} {
		p, _ := windows.UTF16PtrFromString(name)
		format, _, _ := actUser.NewProc("RegisterClipboardFormatW").Call(uintptr(unsafe.Pointer(p)))
		if format == 0 || clipboardMemory(format, []byte{0, 0, 0, 0}) != nil {
			return errNative
		}
	}
	text, err := windows.UTF16FromString(string(raw))
	if err != nil {
		return errNative
	}
	return clipboardMemory(13, unsafe.Slice((*byte)(unsafe.Pointer(&text[0])), len(text)*2)) // CF_UNICODETEXT
}

type actionDocInfo struct {
	Size                   int32
	Name, Output, DataType *uint16
	Type                   uint32
}

// PrintText submits bounded plain text using GDI, never a user-controlled shell
// command or raw printer language. Success is spool submission, not paper output.
func PrintText(printer string, raw []byte) (string, error) {
	if printer == "" || len(printer) > 256 || len(raw) > 1<<20 || !utf8.Valid(raw) || strings.IndexByte(string(raw), 0) >= 0 {
		return "", errNative
	}
	name, err := windows.UTF16PtrFromString(printer)
	if err != nil {
		return "", errNative
	}
	driver, _ := windows.UTF16PtrFromString("WINSPOOL")
	dc, _, _ := actGDI.NewProc("CreateDCW").Call(uintptr(unsafe.Pointer(driver)), uintptr(unsafe.Pointer(name)), 0, 0)
	if dc == 0 {
		return "", errNative
	}
	defer actGDI.NewProc("DeleteDC").Call(dc)
	width, _, _ := actGDI.NewProc("GetDeviceCaps").Call(dc, 8)
	height, _, _ := actGDI.NewProc("GetDeviceCaps").Call(dc, 10)
	dpi, _, _ := actGDI.NewProc("GetDeviceCaps").Call(dc, 90)
	if width < 100 || height < 100 || dpi < 72 || dpi > 2400 {
		return "", errNative
	}
	fontHeight := int(dpi) * 11 / 72
	lineHeight := int(dpi) * 14 / 72
	face, _ := windows.UTF16PtrFromString("Consolas")
	font, _, _ := actGDI.NewProc("CreateFontW").Call(uintptr(-int32(fontHeight)), 0, 0, 0, 400, 0, 0, 0, 1, 0, 0, 0, 0x31, uintptr(unsafe.Pointer(face)))
	if font == 0 {
		return "", errNative
	}
	defer actGDI.NewProc("DeleteObject").Call(font)
	old, _, _ := actGDI.NewProc("SelectObject").Call(dc, font)
	if old == 0 || old == ^uintptr(0) {
		return "", errNative
	}
	defer actGDI.NewProc("SelectObject").Call(dc, old)
	columns := max(10, (int(width)-fontHeight*2)/max(1, fontHeight)) // conservative full-width glyph budget
	rows := max(1, (int(height)-lineHeight*2)/lineHeight)
	lines := []string{}
	for _, line := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n") {
		runes := []rune(strings.ReplaceAll(line, "\t", "    "))
		if len(runes) == 0 {
			lines = append(lines, "")
		}
		for len(runes) > 0 {
			n := min(columns, len(runes))
			lines = append(lines, string(runes[:n]))
			runes = runes[n:]
		}
	}
	if len(lines) > rows*20 {
		return "", errNative
	}
	title, _ := windows.UTF16PtrFromString("Scarlet controlled text")
	doc := actionDocInfo{Name: title}
	doc.Size = int32(unsafe.Sizeof(doc))
	job, _, _ := actGDI.NewProc("StartDocW").Call(dc, uintptr(unsafe.Pointer(&doc)))
	if int32(job) <= 0 {
		return "", errNative
	}
	jobRef := fmt.Sprint(job)
	complete := false
	defer func() {
		if !complete {
			actGDI.NewProc("AbortDoc").Call(dc)
		}
	}()
	for offset := 0; offset < len(lines); offset += rows {
		r, _, _ := actGDI.NewProc("StartPage").Call(dc)
		if int32(r) <= 0 {
			return jobRef, errNative
		}
		for i, line := range lines[offset:min(offset+rows, len(lines))] {
			text := utf16.Encode([]rune(line))
			if len(text) == 0 {
				continue
			}
			r, _, _ = actGDI.NewProc("TextOutW").Call(dc, uintptr(fontHeight), uintptr(lineHeight*(i+1)), uintptr(unsafe.Pointer(&text[0])), uintptr(len(text)))
			if r == 0 {
				return jobRef, errNative
			}
		}
		r, _, _ = actGDI.NewProc("EndPage").Call(dc)
		if int32(r) <= 0 {
			return jobRef, errNative
		}
	}
	r, _, _ := actGDI.NewProc("EndDoc").Call(dc)
	if int32(r) <= 0 {
		return jobRef, errNative
	}
	complete = true
	return jobRef, nil
}
