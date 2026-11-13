//go:build windows

package endpointnativecontrol

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	usbPolicyKey     = `SOFTWARE\Policies\Microsoft\Windows\RemovableStorageDevices\{53f5630d-b6bf-11d0-94f2-00a0c91efb8b}`
	rdpPolicyKey     = `SOFTWARE\Policies\Microsoft\Windows NT\Terminal Services`
	maxPrinterBuffer = 1 << 20
)

var (
	spool             = windows.NewLazySystemDLL("winspool.drv")
	openPrinter       = spool.NewProc("OpenPrinterW")
	closePrinter      = spool.NewProc("ClosePrinter")
	getPrinter        = spool.NewProc("GetPrinterW")
	setPrinter        = spool.NewProc("SetPrinterW")
	getProduct        = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetProductInfo")
	isMDM             = windows.NewLazySystemDLL("MDMRegistration.dll").NewProc("IsDeviceRegisteredWithManagement")
	flushRegistry     = windows.NewLazySystemDLL("advapi32.dll").NewProc("RegFlushKey")
	createRegistryTx  = windows.NewLazySystemDLL("advapi32.dll").NewProc("RegCreateKeyTransactedW")
	createTransaction = windows.NewLazySystemDLL("KtmW32.dll").NewProc("CreateTransaction")
	commitTransaction = windows.NewLazySystemDLL("KtmW32.dll").NewProc("CommitTransaction")
)

func platformRead(ctx context.Context, t Target) (State, error) {
	if err := ctx.Err(); err != nil {
		return State{}, err
	}
	if t.Executor == PrintPause {
		h, err := openLocalQueue(t)
		if err != nil {
			return State{}, err
		}
		defer closePrinter.Call(h)
		return queueState(h, t)
	}
	if err := registryAuthority(t); err != nil {
		return State{}, err
	}
	return registryState(t)
}

func platformChange(ctx context.Context, t Target, previous, next State) (State, error) {
	if err := ctx.Err(); err != nil {
		return State{}, err
	}
	if t.Executor == PrintPause {
		h, err := openLocalQueue(t)
		if err != nil {
			return State{}, err
		}
		defer closePrinter.Call(h)
		current, err := queueState(h, t)
		if err != nil {
			return State{}, err
		}
		if !sameCurrent(current, previous) || current.IdentityDigest != next.IdentityDigest {
			return State{}, ErrConflict
		}
		if err := ctx.Err(); err != nil {
			return State{}, err
		}
		command := uintptr(2) // PRINTER_CONTROL_RESUME; never PURGE or job deletion
		if next.Paused {
			command = 1
		} // PRINTER_CONTROL_PAUSE
		ok, _, nativeErr := setPrinter.Call(h, 0, 0, command)
		if ok == 0 {
			return State{}, nativeError(nativeErr)
		}
		return queueState(h, t)
	}
	if err := registryAuthority(t); err != nil {
		return State{}, err
	}
	current, err := registryState(t)
	if err != nil {
		return State{}, err
	}
	if !sameCurrent(current, previous) {
		return State{}, ErrConflict
	}
	path, name, ok := registryTarget(t.Executor)
	if !ok {
		return State{}, ErrTarget
	}
	return changeRegistryTransaction(ctx, path, name, previous, next)
}

func changeRegistryTransaction(ctx context.Context, path, name string, previous, next State) (State, error) {
	// The target and value names come exclusively from registryTarget above.
	// KTM makes the final check/write indivisible with conflicting registry
	// writers. No nontransactional fallback, ACL change or privilege enabling.
	tx, _, err := createTransaction.Call(0, 0, 0, 0, 0, 5000, 0)
	if tx == 0 || tx == ^uintptr(0) {
		return State{}, nativeError(err)
	}
	defer windows.CloseHandle(windows.Handle(tx)) // rolls back if not committed
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return State{}, ErrTarget
	}
	var k registry.Key
	var disposition uint32
	status, _, _ := createRegistryTx.Call(uintptr(registry.LOCAL_MACHINE), uintptr(unsafe.Pointer(pointer)), 0, 0, 0, uintptr(registry.QUERY_VALUE|registry.SET_VALUE|registry.WOW64_64KEY), 0, uintptr(unsafe.Pointer(&k)), uintptr(unsafe.Pointer(&disposition)), tx, 0)
	if status != 0 {
		return State{}, nativeError(syscall.Errno(status))
	}
	defer k.Close()
	// Recheck the value through the opened handle. A newly created empty key
	// has a new stamp; its absent value is allowed only when the prior key was
	// absent. Existing keys must retain their captured revision.
	opened, err := stateFromKey(k, name)
	if err != nil {
		return State{}, err
	}
	if previous.ChangeStamp == "" {
		if disposition != 1 || opened.Present {
			return State{}, ErrConflict
		}
	} else if disposition != 2 || !sameCurrent(opened, previous) {
		return State{}, ErrConflict
	}
	if err := ctx.Err(); err != nil {
		return State{}, err
	}
	if next.Present {
		if next.Value != 1 {
			return State{}, ErrTarget
		}
		err = k.SetDWordValue(name, 1)
	} else {
		err = k.DeleteValue(name)
	}
	if err != nil {
		return State{}, nativeError(err)
	}
	if err := ctx.Err(); err != nil {
		return State{}, err
	} // uncommitted transaction rolls back
	committed, _, nativeErr := commitTransaction.Call(tx)
	if committed == 0 {
		return State{}, nativeError(nativeErr)
	}
	// Reopen outside the completed transaction for durable/readback evidence.
	readKey, err := registry.OpenKey(registry.LOCAL_MACHINE, path, registry.QUERY_VALUE|registry.SET_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return State{}, ErrReadback
	}
	defer readKey.Close()
	if status, _, _ := flushRegistry.Call(uintptr(readKey)); status != 0 {
		return State{}, ErrReadback
	}
	// Keep an empty key after restoring absence. Deleting the whole key could
	// race another administrator adding an unrelated value and destroy it.
	return stateFromKey(readKey, name)
}

func registryTarget(executor string) (string, string, bool) {
	switch executor {
	case USBWrite:
		return usbPolicyKey, "Deny_Write", true
	case RDPClipboard:
		return rdpPolicyKey, "fDisableClip", true
	}
	return "", "", false
}

func registryAuthority(t Target) error {
	if _, _, ok := registryTarget(t.Executor); !ok {
		return ErrTarget
	}
	v := windows.RtlGetVersion()
	// The initial registry controllers target supported Windows 10/11 client
	// policy editions. No unsupported Server/MDM inference is made.
	if v.MajorVersion != 10 || v.BuildNumber < 19041 || v.ProductType != 1 {
		return ErrUnsupported
	}
	var sku uint32
	if getProduct.Find() != nil {
		return ErrUnsupported
	}
	ok, _, _ := getProduct.Call(uintptr(v.MajorVersion), uintptr(v.MinorVersion), uintptr(v.ServicePackMajor), uintptr(v.ServicePackMinor), uintptr(unsafe.Pointer(&sku)))
	if ok == 0 || !policyEdition(sku) {
		return ErrUnsupported
	}
	if createRegistryTx.Find() != nil || createTransaction.Find() != nil || commitTransaction.Find() != nil || flushRegistry.Find() != nil {
		return ErrUnsupported
	}
	var domain *uint16
	var status uint32
	err := windows.NetGetJoinInformation(nil, &domain, &status)
	if domain != nil {
		defer windows.NetApiBufferFree((*byte)(unsafe.Pointer(domain)))
	}
	if err != nil || status != windows.NetSetupUnjoined && status != windows.NetSetupWorkgroupName {
		return ErrManaged
	}
	// Request no UPN buffer: only the management boolean is inspected, never
	// tenant, domain or user identifiers. Failure is not proof of no management.
	if isMDM.Find() != nil {
		return ErrManaged
	}
	var managed int32
	result, _, _ := isMDM.Call(uintptr(unsafe.Pointer(&managed)), 0, 0)
	if result != 0 || managed != 0 {
		return ErrManaged
	}
	return nil
}

func policyEdition(sku uint32) bool {
	switch sku {
	case 0x04, 0x1b, 0x30, 0x31, 0x79, 0x7a, 0x7d, 0x7e, 0xa1, 0xa2, 0xa4, 0xa5, 0xbc, 0xbf:
		return true
	}
	return false
}

func registryState(t Target) (State, error) {
	path, name, ok := registryTarget(t.Executor)
	if !ok {
		return State{}, ErrTarget
	}
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, path, registry.QUERY_VALUE|registry.SET_VALUE|registry.WOW64_64KEY)
	if errors.Is(err, syscall.ERROR_FILE_NOT_FOUND) || errors.Is(err, syscall.ERROR_PATH_NOT_FOUND) {
		// Probe the nearest existing parent's create-subkey permission without
		// creating anything during Capture/Readback.
		for parent := path; strings.Contains(parent, `\`); {
			parent = parent[:strings.LastIndex(parent, `\`)]
			p, e := registry.OpenKey(registry.LOCAL_MACHINE, parent, registry.CREATE_SUB_KEY|registry.WOW64_64KEY)
			if e == nil {
				p.Close()
				return State{}, nil
			}
			if !errors.Is(e, syscall.ERROR_FILE_NOT_FOUND) && !errors.Is(e, syscall.ERROR_PATH_NOT_FOUND) {
				return State{}, nativeError(e)
			}
		}
		return State{}, ErrUnavailable
	}
	if err != nil {
		return State{}, nativeError(err)
	}
	defer k.Close()
	return stateFromKey(k, name)
}

func stateFromKey(k registry.Key, name string) (State, error) {
	first, err := k.Stat()
	if err != nil {
		return State{}, nativeError(err)
	}
	var data [4]byte
	n, kind, err := k.GetValue(name, data[:])
	state := State{}
	if !errors.Is(err, syscall.ERROR_FILE_NOT_FOUND) {
		if errors.Is(err, syscall.ERROR_MORE_DATA) {
			return State{}, ErrConflict
		}
		if err != nil {
			return State{}, nativeError(err)
		}
		if kind != registry.DWORD || n != 4 {
			return State{}, ErrConflict
		}
		state.Present = true
		state.Value = binary.LittleEndian.Uint32(data[:])
		if state.Value > 1 {
			return State{}, ErrConflict
		}
	}
	last, err := k.Stat()
	if err != nil {
		return State{}, nativeError(err)
	}
	if !first.ModTime().Equal(last.ModTime()) {
		return State{}, ErrConflict
	}
	state.ChangeStamp = fmt.Sprintf("%016x", uint64(last.ModTime().UnixNano()))
	return state, nil
}

func nativeError(err error) error {
	if errors.Is(err, windows.ERROR_TRANSACTIONAL_CONFLICT) || errors.Is(err, windows.ERROR_TRANSACTION_ALREADY_ABORTED) {
		return ErrConflict
	}
	if errors.Is(err, syscall.ERROR_ACCESS_DENIED) || errors.Is(err, syscall.ERROR_PRIVILEGE_NOT_HELD) {
		return ErrPermission
	}
	return ErrUnavailable
}

type printerDefaults struct {
	Datatype, DevMode uintptr
	Access            uint32
}
type printerInfo2 struct {
	Server, Name, Share, Port, Driver, Comment, Location, DevMode, Separator, Processor, Datatype, Parameters, Security uintptr
	Attributes, Priority, DefaultPriority, Start, Until, Status, Jobs, AveragePPM                                       uint32
}

func openLocalQueue(t Target) (uintptr, error) {
	if openPrinter.Find() != nil || closePrinter.Find() != nil || getPrinter.Find() != nil || setPrinter.Find() != nil {
		return 0, ErrUnavailable
	}
	p, err := windows.UTF16PtrFromString(t.PrinterName)
	if err != nil {
		return 0, ErrTarget
	}
	defaults := printerDefaults{Access: 0x0c} // PRINTER_ACCESS_ADMINISTER|USE
	var handle uintptr
	ok, _, nativeErr := openPrinter.Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&handle)), uintptr(unsafe.Pointer(&defaults)))
	if ok == 0 {
		return 0, nativeError(nativeErr)
	}
	if _, err := queueState(handle, t); err != nil {
		closePrinter.Call(handle)
		return 0, err
	}
	return handle, nil
}

func queueState(handle uintptr, t Target) (State, error) {
	var needed uint32
	getPrinter.Call(handle, 2, 0, 0, uintptr(unsafe.Pointer(&needed)))
	if needed < uint32(unsafe.Sizeof(printerInfo2{})) || needed > maxPrinterBuffer {
		return State{}, ErrUnavailable
	}
	buf := make([]byte, needed)
	ok, _, nativeErr := getPrinter.Call(handle, 2, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), uintptr(unsafe.Pointer(&needed)))
	if ok == 0 {
		return State{}, nativeError(nativeErr)
	}
	info := (*printerInfo2)(unsafe.Pointer(&buf[0]))
	// Locally controlled queue only; remote connections/cached server state
	// cannot be promoted into a device-wide pause control.
	if info.Attributes&0x40 == 0 || info.Attributes&0x10 != 0 {
		return State{}, ErrTarget
	}
	values := make([]string, 0, 7)
	for _, pointer := range []uintptr{info.Name, info.Server, info.Share, info.Port, info.Driver, info.Processor, info.Datatype} {
		value, valid := boundedNativeString(buf, pointer)
		if !valid {
			return State{}, ErrUnavailable
		}
		values = append(values, value)
	}
	if !strings.EqualFold(values[0], t.PrinterName) {
		return State{}, ErrTarget
	}
	identity := struct {
		Values                                              []string
		Attributes, Priority, DefaultPriority, Start, Until uint32
	}{values, info.Attributes, info.Priority, info.DefaultPriority, info.Start, info.Until}
	raw, _ := json.Marshal(identity)
	hash := sha256.Sum256(raw)
	state := State{Present: true, Paused: info.Status&1 != 0, IdentityDigest: hex.EncodeToString(hash[:])}
	runtime.KeepAlive(buf)
	return state, nil
}

func boundedNativeString(buf []byte, pointer uintptr) (string, bool) {
	if pointer == 0 {
		return "", true
	}
	if len(buf) < 2 {
		return "", false
	}
	base := uintptr(unsafe.Pointer(&buf[0]))
	if pointer < base || pointer-base >= uintptr(len(buf)) || (pointer-base)%2 != 0 {
		return "", false
	}
	offset := int(pointer - base)
	units := unsafe.Slice((*uint16)(unsafe.Pointer(&buf[offset])), min((len(buf)-offset)/2, 4096))
	for i, u := range units {
		if u == 0 {
			return windows.UTF16ToString(units[:i]), true
		}
	}
	return "", false
}
