//go:build windows

package endpointnative

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const clipboardVolatileKey = "ScarletPlumeClipboardVolatile"

var (
	sessionRegCreate  = windows.NewLazySystemDLL("advapi32.dll").NewProc("RegCreateKeyExW")
	sessionRegCurrent = windows.NewLazySystemDLL("advapi32.dll").NewProc("RegOpenCurrentUser")
)

type windowsClipboardGenerationStore struct {
	sid          *windows.SID
	sidText, key string
}

func newClipboardGenerationStore(i ClipboardSessionIdentity) (*windowsClipboardGenerationStore, error) {
	sid, err := windows.StringToSid(i.UserSID)
	if err != nil {
		return nil, ErrClipboardSession
	}
	hash := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d", i.UserSID, i.AuthenticationID, i.SessionID)))
	if sessionRegCreate.Find() != nil || sessionRegCurrent.Find() != nil {
		return nil, ErrClipboardSession
	}
	return &windowsClipboardGenerationStore{sid: sid, sidText: i.UserSID, key: hex.EncodeToString(hash[:])}, nil
}

func (s *windowsClipboardGenerationStore) user(access uint32) (registry.Key, error) {
	var key registry.Key
	status, _, _ := sessionRegCurrent.Call(uintptr(access|registry.WOW64_64KEY), uintptr(unsafe.Pointer(&key)))
	if status != 0 {
		return 0, ErrClipboardSession
	}
	return key, nil
}

func clipboardGenerationACL(sd *windows.SECURITY_DESCRIPTOR, sid *windows.SID) bool {
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !owner.Equals(sid) && !owner.IsWellKnown(windows.WinLocalSystemSid) {
		return false
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return false
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil || acl.AceCount == 0 {
		return false
	}
	for index := uint32(0); index < uint32(acl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if windows.GetAce(acl, index, &ace) != nil || ace == nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return false
		}
		who := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !who.Equals(sid) && !who.IsWellKnown(windows.WinLocalSystemSid) {
			return false
		}
	}
	return true
}

func (s *windowsClipboardGenerationStore) trusted(key registry.Key) bool {
	sd, err := windows.GetSecurityInfo(windows.Handle(key), windows.SE_REGISTRY_KEY, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	return err == nil && clipboardGenerationACL(sd, s.sid)
}

func (s *windowsClipboardGenerationStore) load() ([]byte, error) {
	user, err := s.user(registry.QUERY_VALUE)
	if err != nil {
		return nil, err
	}
	defer user.Close()
	base, err := registry.OpenKey(user, `Software\`+clipboardVolatileKey, registry.READ|registry.WOW64_64KEY)
	if errors.Is(err, syscall.ERROR_FILE_NOT_FOUND) || errors.Is(err, syscall.ERROR_PATH_NOT_FOUND) {
		return nil, errClipboardGenerationMissing
	}
	if err != nil {
		return nil, ErrClipboardSession
	}
	defer base.Close()
	if !s.trusted(base) {
		return nil, ErrClipboardSession
	}
	key, err := registry.OpenKey(base, s.key, registry.READ|registry.WOW64_64KEY)
	if errors.Is(err, syscall.ERROR_FILE_NOT_FOUND) || errors.Is(err, syscall.ERROR_PATH_NOT_FOUND) {
		return nil, errClipboardGenerationMissing
	}
	if err != nil {
		return nil, ErrClipboardSession
	}
	defer key.Close()
	if !s.trusted(key) {
		return nil, ErrClipboardSession
	}
	raw := make([]byte, 32)
	n, kind, err := key.GetValue("Generation", raw)
	if errors.Is(err, syscall.ERROR_FILE_NOT_FOUND) {
		return nil, errClipboardGenerationMissing
	}
	if err != nil || n != 32 || kind != registry.BINARY {
		clear(raw)
		return nil, ErrClipboardSession
	}
	return raw, nil
}

func (s *windowsClipboardGenerationStore) create(parent registry.Key, name string) (registry.Key, error) {
	sd, err := windows.SecurityDescriptorFromString("O:" + s.sidText + "D:P(A;CI;KA;;;SY)(A;CI;KA;;;" + s.sidText + ")")
	if err != nil {
		return 0, ErrClipboardSession
	}
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, ErrClipboardSession
	}
	var key registry.Key
	// REG_OPTION_VOLATILE=1. No policy/machine keys, backup privileges, links,
	// persistent fallback, or writes to existing foreign security descriptors.
	status, _, _ := sessionRegCreate.Call(uintptr(parent), uintptr(unsafe.Pointer(p)), 0, 0, 1, uintptr(registry.READ|registry.WRITE|registry.WOW64_64KEY), uintptr(unsafe.Pointer(&sa)), uintptr(unsafe.Pointer(&key)), 0)
	runtime.KeepAlive(sd)
	if status != 0 {
		return 0, ErrClipboardSession
	}
	if !s.trusted(key) {
		key.Close()
		return 0, ErrClipboardSession
	}
	return key, nil
}

func (s *windowsClipboardGenerationStore) saveNew(raw []byte) error {
	if len(raw) != 32 {
		return ErrClipboardSession
	}
	user, err := s.user(registry.QUERY_VALUE | registry.CREATE_SUB_KEY)
	if err != nil {
		return err
	}
	defer user.Close()
	software, err := registry.OpenKey(user, "Software", registry.CREATE_SUB_KEY|registry.WOW64_64KEY)
	if err != nil {
		return ErrClipboardSession
	}
	defer software.Close()
	base, err := s.create(software, clipboardVolatileKey)
	if err != nil {
		return err
	}
	defer base.Close()
	key, err := s.create(base, s.key)
	if err != nil {
		return err
	}
	defer key.Close()
	if _, _, err := key.GetValue("Generation", nil); !errors.Is(err, syscall.ERROR_FILE_NOT_FOUND) {
		return ErrClipboardSession
	}
	if key.SetBinaryValue("Generation", raw) != nil {
		return ErrClipboardSession
	}
	return nil
}

func (s *windowsClipboardGenerationStore) withLock(fn func() error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	sd, err := windows.SecurityDescriptorFromString("O:" + s.sidText + "D:P(A;;GA;;;SY)(A;;GA;;;" + s.sidText + ")")
	if err != nil {
		return ErrClipboardSession
	}
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	name, err := windows.UTF16PtrFromString(`Local\ScarletPlumeClipboardGeneration.` + s.key)
	if err != nil {
		return ErrClipboardSession
	}
	mutex, err := windows.CreateMutexEx(&sa, name, 0, windows.SYNCHRONIZE|windows.MUTEX_MODIFY_STATE|windows.READ_CONTROL)
	runtime.KeepAlive(sd)
	if mutex == 0 || err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return ErrClipboardSession
	}
	defer windows.CloseHandle(mutex)
	security, err := windows.GetSecurityInfo(mutex, windows.SE_KERNEL_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil || !clipboardGenerationACL(security, s.sid) {
		return ErrClipboardSession
	}
	state, err := windows.WaitForSingleObject(mutex, 1000)
	if err != nil || state != windows.WAIT_OBJECT_0 && state != windows.WAIT_ABANDONED {
		return ErrClipboardSession
	}
	defer windows.ReleaseMutex(mutex)
	return fn()
}
