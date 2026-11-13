//go:build windows

package endpointnative

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	sessionSequence          = user32.NewProc("GetClipboardSequenceNumber")
	sessionOwner             = user32.NewProc("GetClipboardOwner")
	sessionThreadDesktop     = user32.NewProc("GetThreadDesktop")
	sessionEmpty             = user32.NewProc("EmptyClipboard")
	clipboardSessionWindows  sync.Map
	clipboardSessionCallback = syscall.NewCallback(clipboardSessionWindowProc)
)

type clipboardTokenStatistics struct {
	TokenID, AuthenticationID                                                           windows.LUID
	ExpirationTime                                                                      int64
	TokenType, ImpersonationLevel, DynamicCharged, DynamicAvailable, Groups, Privileges uint32
	ModifiedID                                                                          windows.LUID
}

// CurrentClipboardSession checks the primary interactive identity and obtains
// its fixed HKCU volatile boot generation. It does not read the clipboard,
// enumerate applications or alter policy/machine configuration.
func CurrentClipboardSession() (ClipboardSessionIdentity, error) {
	return currentClipboardSession(true)
}

func currentClipboardSession(allowBootInit bool) (ClipboardSessionIdentity, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	for _, p := range []*windows.LazyProc{sessionThreadDesktop, getUserObjectInformation, getProcessWindowStation} {
		if p.Find() != nil {
			return ClipboardSessionIdentity{}, ErrClipboardSession
		}
	}
	// Process identity must be the actor; do not act through an impersonated
	// thread whose identity differs from the primary interactive user process.
	var impersonated windows.Token
	if err := windows.OpenThreadToken(windows.CurrentThread(), windows.TOKEN_QUERY, true, &impersonated); err == nil {
		impersonated.Close()
		return ClipboardSessionIdentity{}, ErrClipboardSession
	} else if !errors.Is(err, windows.ERROR_NO_TOKEN) {
		return ClipboardSessionIdentity{}, ErrClipboardSession
	}
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil {
		return ClipboardSessionIdentity{}, ErrClipboardSession
	}
	var stats clipboardTokenStatistics
	var length uint32
	if windows.GetTokenInformation(token, windows.TokenStatistics, (*byte)(unsafe.Pointer(&stats)), uint32(unsafe.Sizeof(stats)), &length) != nil || length != uint32(unsafe.Sizeof(stats)) || stats.TokenType != 1 {
		return ClipboardSessionIdentity{}, ErrClipboardSession
	}
	var id uint32
	if windows.GetTokenInformation(token, windows.TokenSessionId, (*byte)(unsafe.Pointer(&id)), 4, &length) != nil || length != 4 {
		return ClipboardSessionIdentity{}, ErrClipboardSession
	}
	var processID uint32
	if windows.ProcessIdToSessionId(uint32(os.Getpid()), &processID) != nil || processID != id {
		return ClipboardSessionIdentity{}, ErrClipboardSession
	}
	i := ClipboardSessionIdentity{UserSID: user.User.Sid.String(), AuthenticationID: fmt.Sprintf("%08x:%08x", uint32(stats.AuthenticationID.HighPart), stats.AuthenticationID.LowPart), SessionID: id}
	station, _, _ := getProcessWindowStation.Call()
	var flags struct {
		Inherit, Reserved int32
		Flags             uint32
	}
	visible, _, _ := getUserObjectInformation.Call(station, 1, uintptr(unsafe.Pointer(&flags)), unsafe.Sizeof(flags), uintptr(unsafe.Pointer(&length)))
	desktop, _, _ := sessionThreadDesktop.Call(uintptr(windows.GetCurrentThreadId())) // borrowed, do not close
	var input int32
	receivesInput, _, _ := getUserObjectInformation.Call(desktop, 6, uintptr(unsafe.Pointer(&input)), 4, uintptr(unsafe.Pointer(&length))) // UOI_IO
	if !allowedClipboardSession(i, visible != 0 && flags.Flags&1 != 0, receivesInput != 0 && input != 0) {
		return ClipboardSessionIdentity{}, ErrClipboardSession
	}
	store, err := newClipboardGenerationStore(i)
	if err != nil {
		return ClipboardSessionIdentity{}, ErrClipboardSession
	}
	i.BootGeneration, err = clipboardBootGeneration(allowBootInit, store)
	if err != nil {
		return ClipboardSessionIdentity{}, ErrClipboardSession
	}
	return i, nil
}

type windowsClipboardSessionAPI struct{ locked bool }

func (a *windowsClipboardSessionAPI) identity() (ClipboardSessionIdentity, error) {
	return currentClipboardSession(!a.locked)
}
func (windowsClipboardSessionAPI) thread() uint32 { return windows.GetCurrentThreadId() }
func (a *windowsClipboardSessionAPI) open(window uintptr) bool {
	ok, _, _ := openClipboard.Call(window)
	if ok != 0 {
		a.locked = true
	}
	return ok != 0
}
func (a *windowsClipboardSessionAPI) close() bool {
	ok, _, _ := closeClipboard.Call()
	if ok != 0 {
		a.locked = false
	}
	return ok != 0
}
func (windowsClipboardSessionAPI) sequence() uint32 {
	s, _, _ := sessionSequence.Call()
	return uint32(s)
}
func (windowsClipboardSessionAPI) owner() uintptr { o, _, _ := sessionOwner.Call(); return o }
func (windowsClipboardSessionAPI) empty() bool    { ok, _, _ := sessionEmpty.Call(); return ok != 0 }
func (windowsClipboardSessionAPI) formats() ([]uint32, bool) {
	formats := make([]uint32, 0, 8)
	format := uintptr(0)
	for i := 0; i < 64; i++ {
		setLastError.Call(0)
		next, _, err := enumClipboardFormats.Call(format)
		if next == 0 {
			return formats, err == syscall.Errno(0)
		}
		formats = append(formats, uint32(next))
		format = next
	}
	return formats, false
}
func (windowsClipboardSessionAPI) text() ([]byte, bool) {
	// GetClipboardData can synchronously request delayed rendering from another
	// application. This call has no cancellable deadline; do not fake a timeout
	// while a goroutine continues reading content or holds the clipboard open.
	h, _, _ := getClipboardData.Call(cfUnicodeText)
	if h == 0 {
		return nil, false
	}
	size, _, _ := globalSize.Call(h)
	if size < 2 {
		return nil, false
	}
	address, _, _ := globalLock.Call(h)
	if address == 0 {
		return nil, false
	}
	defer globalUnlock.Call(h)
	units := make([]uint16, min(size/2, maxClipboardUnits))
	defer clear(units)
	var copied uintptr
	bytes := uintptr(len(units)) * 2
	if windows.ReadProcessMemory(windows.CurrentProcess(), address, (*byte)(unsafe.Pointer(&units[0])), bytes, &copied) != nil || copied != bytes {
		return nil, false
	}
	text, complete := clipboardText(units)
	return text, complete && size%2 == 0
}

type clipboardSessionWindow struct {
	listener clipboardSessionListener
	err      error
}

// WatchClipboardSession emits only post-registration changes while approved
// capture is enabled. emit runs synchronously on this locked message thread,
// with the clipboard CLOSED. Persist metadata intent/recheck local policy here;
// do not wait for network delivery. Text is wiped when emit returns, including
// on error. HTTP/outbox workers must receive anonymous metadata only.
func WatchClipboardSession(ctx context.Context, shouldCapture func() bool, emit func(ClipboardSessionEvent) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if shouldCapture == nil || emit == nil {
		return ErrClipboardSession
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	identity, err := CurrentClipboardSession()
	if err != nil {
		return err
	}
	for _, p := range []*windows.LazyProc{sessionSequence, sessionOwner, sessionEmpty, addClipboardListener, removeClipboardListener, enumClipboardFormats, getClipboardData, globalSize, globalLock, globalUnlock} {
		if p.Find() != nil {
			return ErrClipboardSession
		}
	}
	name, _ := windows.UTF16PtrFromString("ScarletUserClipboardSession" + strconv.FormatUint(windowNumber.Add(1), 10))
	instance, _, _ := getModuleHandle.Call(0)
	class := windowClass{Proc: clipboardSessionCallback, Instance: instance, Name: name}
	atom, _, _ := registerClass.Call(uintptr(unsafe.Pointer(&class)))
	if atom == 0 {
		return ErrClipboardSession
	}
	defer unregisterClass.Call(uintptr(unsafe.Pointer(name)), instance)
	hwnd, _, _ := createWindow.Call(0, uintptr(unsafe.Pointer(name)), 0, 0, 0, 0, 0, 0, 0, 0, instance, 0)
	if hwnd == 0 {
		return ErrClipboardSession
	}
	defer destroyWindow.Call(hwnd)
	state := &clipboardSessionWindow{listener: clipboardSessionListener{ctx: ctx, api: &windowsClipboardSessionAPI{}, window: hwnd, identity: identity, shouldCapture: shouldCapture, emit: emit}}
	clipboardSessionWindows.Store(hwnd, state)
	defer clipboardSessionWindows.Delete(hwnd)
	ok, _, _ := addClipboardListener.Call(hwnd)
	if ok == 0 {
		return ErrClipboardSession
	}
	defer removeClipboardListener.Call(hwnd)
	// No GetClipboardData/format enumeration at startup, even if registration
	// delivers a queued update about pre-existing data. Initial sequence is a
	// metadata-only baseline; identical notifications are ignored.
	if err := state.listener.ready(); err != nil {
		return err
	}
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
			return ErrClipboardSession
		}
		if r == 0 {
			if state.err != nil {
				return state.err
			}
			return ctx.Err()
		}
		translateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		dispatchMessage.Call(uintptr(unsafe.Pointer(&msg)))
	}
}

func clipboardSessionWindowProc(hwnd uintptr, message uint32, wParam, lParam uintptr) uintptr {
	switch message {
	case wmClipboardUpdate:
		if value, ok := clipboardSessionWindows.Load(hwnd); ok {
			s := value.(*clipboardSessionWindow)
			if s.listener.ctx.Err() != nil || s.err != nil {
				return 0
			}
			s.err = s.listener.notify()
			if s.err != nil {
				postMessage.Call(hwnd, wmClose, 0, 0)
			}
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

func ClearClipboardIfCurrent(ctx context.Context, match ClipboardMatch) (ClipboardClearResult, error) {
	return clearClipboardSession(ctx, match)
}
