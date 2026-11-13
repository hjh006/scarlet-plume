//go:build windows

package endpointnative

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32                   = windows.NewLazySystemDLL("user32.dll")
	kernel32                 = windows.NewLazySystemDLL("kernel32.dll")
	winspool                 = windows.NewLazySystemDLL("winspool.drv")
	registerClass            = user32.NewProc("RegisterClassW")
	unregisterClass          = user32.NewProc("UnregisterClassW")
	createWindow             = user32.NewProc("CreateWindowExW")
	destroyWindow            = user32.NewProc("DestroyWindow")
	defWindowProc            = user32.NewProc("DefWindowProcW")
	getMessage               = user32.NewProc("GetMessageW")
	translateMessage         = user32.NewProc("TranslateMessage")
	dispatchMessage          = user32.NewProc("DispatchMessageW")
	postMessage              = user32.NewProc("PostMessageW")
	postQuitMessage          = user32.NewProc("PostQuitMessage")
	addClipboardListener     = user32.NewProc("AddClipboardFormatListener")
	removeClipboardListener  = user32.NewProc("RemoveClipboardFormatListener")
	openClipboard            = user32.NewProc("OpenClipboard")
	closeClipboard           = user32.NewProc("CloseClipboard")
	getClipboardData         = user32.NewProc("GetClipboardData")
	enumClipboardFormats     = user32.NewProc("EnumClipboardFormats")
	getProcessWindowStation  = user32.NewProc("GetProcessWindowStation")
	getUserObjectInformation = user32.NewProc("GetUserObjectInformationW")
	globalSize               = kernel32.NewProc("GlobalSize")
	globalLock               = kernel32.NewProc("GlobalLock")
	globalUnlock             = kernel32.NewProc("GlobalUnlock")
	setLastError             = kernel32.NewProc("SetLastError")
	getModuleHandle          = kernel32.NewProc("GetModuleHandleW")
	enumPrinters             = winspool.NewProc("EnumPrintersW")
	openPrinter              = winspool.NewProc("OpenPrinterW")
	closePrinter             = winspool.NewProc("ClosePrinter")
	enumJobs                 = winspool.NewProc("EnumJobsW")
	clipboardWindows         sync.Map
	windowNumber             atomic.Uint64
	windowCallback           = syscall.NewCallback(clipboardWindowProc)
)

const (
	wmClose           = 0x0010
	wmDestroy         = 0x0002
	wmClipboardUpdate = 0x031d
	cfUnicodeText     = 13
	maxNativeBuffer   = 1024 * 1024
	maxPrinters       = 32
	maxPrintJobs      = 64
)

// Watch observes the current interactive user's Windows session. It does not
// read the existing clipboard on startup. emit is serialized and must inspect
// or anonymize raw fields before returning; no raw observation is persisted.
// Native spooler calls can block; cancellation cannot interrupt a Windows RPC.
func Watch(ctx context.Context, emit func(Observation) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if emit == nil {
		return errors.New("native endpoint observer requires an emitter")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	events := make(chan Observation, 32)
	failures := make(chan error, 1)
	send := func(o Observation) bool {
		select {
		case events <- o:
			return true
		case <-ctx.Done():
			return false
		}
	}
	go watchVolumes(ctx, send)
	go watchPrintJobs(ctx, send)
	go func() { failures <- watchClipboard(ctx, send) }()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-failures:
			if err == nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return errors.New("native clipboard observer stopped")
			}
			return err
		case o := <-events:
			err := emit(o)
			clear(o.Text)
			if err != nil {
				return err
			}
		}
	}
}

// GetDriveType classifies removable-media volumes, not the USB bus. Fixed
// USB disks and non-drive-letter mounts are outside this observer's coverage.
func removableVolumes() (map[string]bool, error) {
	var roots [512]uint16 // at most 26 drive-letter roots, plus double NUL
	n, err := windows.GetLogicalDriveStrings(uint32(len(roots)), &roots[0])
	if err != nil || n == 0 || n >= uint32(len(roots)) {
		return nil, errors.New("native removable volume enumeration unavailable")
	}
	result := map[string]bool{}
	for start, i := 0, 0; i < int(n); i++ {
		if roots[i] != 0 {
			continue
		}
		if i == start {
			break
		}
		if windows.GetDriveType(&roots[start]) == windows.DRIVE_REMOVABLE {
			result[windows.UTF16ToString(roots[start:i])] = true
		}
		start = i + 1
	}
	return result, nil
}

func watchVolumes(ctx context.Context, send func(Observation) bool) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	var previous map[string]bool
	var sequence uint64
	unavailable := false
	for {
		current, err := removableVolumes()
		if err != nil {
			if !unavailable && !send(Observation{Channel: "usb", Kind: "observer_unavailable"}) {
				return
			}
			unavailable = true
			previous = nil // a later baseline cannot prove what happened in the gap
		} else {
			unavailable = false
			if previous != nil {
				for _, root := range changedVolumes(previous, current) {
					sequence++
					change := "removed:"
					if current[root] {
						change = "arrived:"
					}
					if !send(Observation{Channel: "usb", Kind: "device_change", RawTarget: root, Sequence: change + strconv.FormatUint(sequence, 10)}) {
						return
					}
				}
			}
			previous = current
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

type printerInfo4 struct {
	Name, Server uintptr
	Attributes   uint32
}

type jobInfo1 struct {
	ID                                                     uint32
	Printer, Machine, User, Document, Datatype, StatusText uintptr
	Status, Priority, Position, TotalPages, PagesPrinted   uint32
	Submitted                                              windows.Systemtime
}

func printerNames() ([]string, error) {
	var needed, count uint32
	// LOCAL|CONNECTIONS, level 4: installed queues only, not domain discovery.
	ok, _, _ := enumPrinters.Call(6, 0, 4, 0, 0, uintptr(unsafe.Pointer(&needed)), uintptr(unsafe.Pointer(&count)))
	if needed == 0 {
		if ok == 0 {
			return nil, errors.New("native printer enumeration unavailable")
		}
		return nil, nil
	}
	if needed > maxNativeBuffer {
		return nil, errors.New("native printer enumeration exceeds budget")
	}
	buf := make([]byte, needed)
	ok, _, _ = enumPrinters.Call(6, 0, 4, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), uintptr(unsafe.Pointer(&needed)), uintptr(unsafe.Pointer(&count)))
	if ok == 0 || uint64(count)*uint64(unsafe.Sizeof(printerInfo4{})) > uint64(len(buf)) || count > maxPrinters {
		return nil, errors.New("native printer enumeration unavailable or exceeds budget")
	}
	names := make([]string, 0, count)
	for i := uint32(0); i < count; i++ {
		p := (*printerInfo4)(unsafe.Pointer(&buf[uintptr(i)*unsafe.Sizeof(printerInfo4{})]))
		name, valid := nativeUTF16String(buf, p.Name, 1024)
		if !valid {
			return nil, errors.New("native printer metadata invalid")
		}
		names = append(names, name)
	}
	runtime.KeepAlive(buf)
	return names, nil
}

// Native pointers refer back into the caller-owned buffer. Never scan an
// unbounded pointer (queue names may be supplied by a remote print provider).
func nativeUTF16String(buf []byte, ptr uintptr, limit int) (string, bool) {
	if len(buf) == 0 {
		return "", false
	}
	base := uintptr(unsafe.Pointer(&buf[0]))
	if ptr < base || ptr-base >= uintptr(len(buf)) || (ptr-base)%2 != 0 {
		return "", false
	}
	offset := int(ptr - base)
	n := min((len(buf)-offset)/2, limit)
	units := unsafe.Slice((*uint16)(unsafe.Pointer(&buf[offset])), n)
	for i, u := range units {
		if u == 0 {
			return windows.UTF16ToString(units[:i]), true
		}
	}
	return "", false
}

func printerJobs(name string) ([]Observation, error) {
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, errors.New("native printer metadata invalid")
	}
	var handle uintptr
	ok, _, _ := openPrinter.Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&handle)), 0)
	if ok == 0 {
		return nil, errors.New("native printer queue unavailable")
	}
	defer closePrinter.Call(handle)
	var needed, count uint32
	ok, _, _ = enumJobs.Call(handle, 0, maxPrintJobs, 1, 0, 0, uintptr(unsafe.Pointer(&needed)), uintptr(unsafe.Pointer(&count)))
	if needed == 0 {
		if ok == 0 {
			return nil, errors.New("native print job enumeration unavailable")
		}
		return nil, nil
	}
	if needed > maxNativeBuffer {
		return nil, errors.New("native print job enumeration exceeds budget")
	}
	buf := make([]byte, needed)
	ok, _, _ = enumJobs.Call(handle, 0, maxPrintJobs, 1, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), uintptr(unsafe.Pointer(&needed)), uintptr(unsafe.Pointer(&count)))
	if ok == 0 || count > maxPrintJobs || uint64(count)*uint64(unsafe.Sizeof(jobInfo1{})) > uint64(len(buf)) {
		return nil, errors.New("native print job enumeration unavailable")
	}
	result := make([]Observation, 0, count)
	for i := uint32(0); i < count; i++ {
		job := (*jobInfo1)(unsafe.Pointer(&buf[uintptr(i)*unsafe.Sizeof(jobInfo1{})]))
		t := job.Submitted
		seq := fmt.Sprintf("%d:%04d%02d%02dT%02d%02d%02d.%03d", job.ID, t.Year, t.Month, t.Day, t.Hour, t.Minute, t.Second, t.Milliseconds)
		// Never dereference document name, account, machine or spool content.
		result = append(result, Observation{Channel: "print", Kind: "job_observed", RawTarget: name, Sequence: seq})
	}
	runtime.KeepAlive(buf)
	return result, nil
}

func watchPrintJobs(ctx context.Context, send func(Observation) bool) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	var previous map[string]bool
	unavailable := false
	for {
		names, err := printerNames()
		current := map[string]bool{}
		var observed []Observation
		if err == nil {
			for _, name := range names {
				jobs, e := printerJobs(name)
				if e != nil {
					err = e
					continue
				}
				for _, o := range jobs {
					id := o.RawTarget + "\x00" + o.Sequence
					current[id] = true
					if previous != nil && !previous[id] {
						observed = append(observed, o)
					}
				}
			}
		}
		if err != nil {
			if !unavailable && !send(Observation{Channel: "print", Kind: "observer_unavailable"}) {
				return
			}
			unavailable = true
			previous = nil
		} else {
			unavailable = false
			for _, o := range observed {
				if !send(o) {
					return
				}
			}
			previous = current
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

type windowClass struct {
	Style                              uint32
	Proc                               uintptr
	ClassExtra, WindowExtra            int32
	Instance, Icon, Cursor, Background uintptr
	Menu, Name                         *uint16
}
type windowMessage struct {
	Window         uintptr
	Message        uint32
	WParam, LParam uintptr
	Time           uint32
	X, Y           int32
	Private        uint32
}
type clipboardState struct {
	send     func(Observation) bool
	sequence uint64
}

func watchClipboard(ctx context.Context, send func(Observation) bool) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	// A service's session-0 clipboard is not the logged-in user's clipboard.
	station, _, _ := getProcessWindowStation.Call()
	var stationFlags struct {
		Inherit, Reserved int32
		Flags             uint32
	}
	var needed uint32
	visible, _, _ := getUserObjectInformation.Call(station, 1, uintptr(unsafe.Pointer(&stationFlags)), unsafe.Sizeof(stationFlags), uintptr(unsafe.Pointer(&needed)))
	if visible == 0 || stationFlags.Flags&1 == 0 {
		return errors.New("native clipboard observer requires an interactive user session")
	}
	name, _ := windows.UTF16PtrFromString("ScarletEndpointObserver" + strconv.FormatUint(windowNumber.Add(1), 10))
	instance, _, _ := getModuleHandle.Call(0)
	class := windowClass{Proc: windowCallback, Instance: instance, Name: name}
	atom, _, _ := registerClass.Call(uintptr(unsafe.Pointer(&class)))
	if atom == 0 {
		return errors.New("native clipboard window unavailable")
	}
	defer unregisterClass.Call(uintptr(unsafe.Pointer(name)), instance)
	// Invisible top-level window; no input hooks, ownership or clipboard writes.
	hwnd, _, _ := createWindow.Call(0, uintptr(unsafe.Pointer(name)), 0, 0, 0, 0, 0, 0, 0, 0, instance, 0)
	if hwnd == 0 {
		return errors.New("native clipboard window unavailable")
	}
	clipboardWindows.Store(hwnd, &clipboardState{send: send})
	defer clipboardWindows.Delete(hwnd)
	defer destroyWindow.Call(hwnd)
	ok, _, _ := addClipboardListener.Call(hwnd)
	if ok == 0 {
		return errors.New("native clipboard listener unavailable")
	}
	defer removeClipboardListener.Call(hwnd)
	// Registration creates no read of the pre-existing clipboard. Only an actual
	// subsequent WM_CLIPBOARDUPDATE invokes captureClipboard.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			postMessage.Call(hwnd, wmClose, 0, 0)
		case <-done:
		}
	}()
	var msg windowMessage
	for {
		r, _, _ := getMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(r) == -1 {
			return errors.New("native clipboard message loop unavailable")
		}
		if r == 0 {
			return nil
		}
		translateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		dispatchMessage.Call(uintptr(unsafe.Pointer(&msg)))
	}
}

func clipboardWindowProc(hwnd uintptr, message uint32, wParam, lParam uintptr) uintptr {
	switch message {
	case wmClipboardUpdate:
		if value, ok := clipboardWindows.Load(hwnd); ok {
			state := value.(*clipboardState)
			state.sequence++
			o := captureClipboard(hwnd)
			o.Sequence = strconv.FormatUint(state.sequence, 10)
			state.send(o)
		}
		return 0
	case wmClose:
		destroyWindow.Call(hwnd)
		postQuitMessage.Call(0)
		return 0
	case wmDestroy:
		return 0
	}
	r, _, _ := defWindowProc.Call(hwnd, uintptr(message), wParam, lParam)
	return r
}

func captureClipboard(hwnd uintptr) Observation {
	o := Observation{Channel: "clipboard", Kind: "content_changed", RawTarget: "interactive_session_clipboard"}
	ok, _, _ := openClipboard.Call(hwnd)
	if ok == 0 {
		return o
	} // lock contention is unknown, never an empty safe value
	defer closeClipboard.Call()
	handle, _, _ := getClipboardData.Call(cfUnicodeText)
	if handle == 0 {
		return o
	}
	size, _, _ := globalSize.Call(handle)
	if size < 2 {
		return o
	}
	pointer, _, _ := globalLock.Call(handle)
	if pointer == 0 {
		return o
	}
	defer globalUnlock.Call(handle)
	units := make([]uint16, min(size/2, maxClipboardUnits))
	defer clear(units)
	// GlobalLock returns an OS address. Read only this locked allocation in our
	// own process into a bounded Go buffer; do not construct a Go pointer from
	// an integer address or inspect another process's memory.
	var copied uintptr
	bytes := uintptr(len(units)) * 2
	if err := windows.ReadProcessMemory(windows.CurrentProcess(), pointer, (*byte)(unsafe.Pointer(&units[0])), bytes, &copied); err != nil || copied != bytes {
		return o
	}
	o.Text, o.Complete = clipboardText(units)
	// An image, file list, HTML/custom representation can contain additional
	// content. Never claim full capture just because Unicode text is available.
	format := uintptr(0)
	ended := false
	for i := 0; i < 64; i++ {
		setLastError.Call(0)
		var err error
		format, _, err = enumClipboardFormats.Call(format)
		if format == 0 {
			ended = err == syscall.Errno(0)
			break
		}
		if format != 1 && format != 7 && format != 13 && format != 16 {
			o.Complete = false
		}
	}
	o.Complete = o.Complete && ended && size%2 == 0
	return o
}
