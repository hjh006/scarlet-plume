//go:build windows

package endpointsecure

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

func trustedOwnerAndACL(sd *windows.SECURITY_DESCRIPTOR) bool {
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !(owner.IsWellKnown(windows.WinLocalSystemSid) || owner.IsWellKnown(windows.WinBuiltinAdministratorsSid)) {
		return false
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil || acl.AceCount == 0 {
		return false
	}
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if windows.GetAce(acl, i, &ace) != nil || ace == nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return false
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !(sid.IsWellKnown(windows.WinLocalSystemSid) || sid.IsWellKnown(windows.WinBuiltinAdministratorsSid)) {
			return false
		}
	}
	return true
}
func protectPath(path string) error {
	sd, err := windows.SecurityDescriptorFromString("O:BAD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)")
	if err != nil {
		return ErrUnavailable
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return ErrUnavailable
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return ErrUnavailable
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, owner, nil, acl, nil)
}
func secureDirectory(path string) error {
	if !windows.GetCurrentProcessToken().IsElevated() {
		return ErrUnavailable
	}
	programData, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, 0)
	if err != nil {
		return ErrUnavailable
	}
	parent := filepath.Join(programData, "ScarletPlume")
	expected := filepath.Join(parent, "EndpointNative")
	if !strings.EqualFold(filepath.Clean(path), expected) {
		return ErrUnavailable
	}
	// Protect the parent before creating the service directory. Refuse an
	// existing user-owned or broadly writable tree rather than promote it into
	// a SYSTEM service/config location.
	for _, directory := range []string{parent, expected} {
		sd, e := windows.SecurityDescriptorFromString("O:BAD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)")
		if e != nil {
			return ErrUnavailable
		}
		attributes := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
		p, e := windows.UTF16PtrFromString(directory)
		if e != nil {
			return ErrUnavailable
		}
		err := windows.CreateDirectory(p, &attributes)
		runtime.KeepAlive(sd)
		if os.IsExist(err) {
			sd, e := windows.GetNamedSecurityInfo(directory, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
			if e != nil || !trustedOwnerAndACL(sd) {
				return ErrUnavailable
			}
		} else if err != nil {
			return ErrUnavailable
		}
		flags, e := windows.GetFileAttributes(p)
		if e != nil || flags&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || flags&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
			return ErrUnavailable
		}
		if protectPath(directory) != nil {
			return ErrUnavailable
		}
	}
	return nil
}
func secureExistingFile(f *os.File) error {
	// An opened handle normally follows a file symlink. Check its original
	// private-root entry too, so a reparse entry cannot be hidden by the final
	// target's otherwise trusted owner/DACL.
	p, e := windows.UTF16PtrFromString(f.Name())
	if e != nil {
		return ErrUnavailable
	}
	attributes, e := windows.GetFileAttributes(p)
	if e != nil || attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return ErrUnavailable
	}
	var information windows.ByHandleFileInformation
	if windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &information) != nil || information.NumberOfLinks != 1 || information.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return ErrUnavailable
	}
	sd, err := windows.GetSecurityInfo(windows.Handle(f.Fd()), windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil || !trustedOwnerAndACL(sd) {
		return ErrUnavailable
	}
	return nil
}
func secureCreatedFile(f *os.File, _ string) error {
	return secureExistingFile(f)
}

func createPrivateFile(root *os.Root, name string) (*os.File, error) {
	if !validName(name) {
		return nil, ErrUnavailable
	}
	sd, err := windows.SecurityDescriptorFromString("O:BAD:P(A;;FA;;;SY)(A;;FA;;;BA)")
	if err != nil {
		return nil, ErrUnavailable
	}
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	path := filepath.Join(root.Name(), name)
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, ErrUnavailable
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, &sa, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	runtime.KeepAlive(sd)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(h), path)
	if secureExistingFile(f) != nil {
		f.Close()
		return nil, ErrUnavailable
	}
	return f, nil
}

func acquireStoreLock(root *os.Root) (*os.File, error) {
	f, err := createPrivateFile(root, "runtime.lock")
	if os.IsExist(err) {
		f, err = root.OpenFile("runtime.lock", os.O_RDWR, 0)
	}
	if err != nil {
		return nil, ErrUnavailable
	}
	if secureExistingFile(f) != nil {
		f.Close()
		return nil, ErrUnavailable
	}
	var overlapped windows.Overlapped
	if windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlapped) != nil {
		f.Close()
		return nil, ErrUnavailable
	}
	return f, nil // Windows releases the exclusive range lock when the handle closes.
}

func commitExecutable(from, to string) error {
	a, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return ErrUnavailable
	}
	b, err := windows.UTF16PtrFromString(to)
	if err != nil {
		return ErrUnavailable
	}
	// Do not replace an existing installation. Flush the move before SCM registration.
	if windows.MoveFileEx(a, b, windows.MOVEFILE_WRITE_THROUGH) != nil {
		return ErrUnavailable
	}
	return nil
}

func blob(raw []byte) windows.DataBlob {
	b := windows.DataBlob{Size: uint32(len(raw))}
	if len(raw) > 0 {
		b.Data = &raw[0]
	}
	return b
}
func seal(raw []byte) ([]byte, error) {
	input := blob(raw)
	var output windows.DataBlob
	if windows.CryptProtectData(&input, nil, nil, 0, nil, 0x5, &output) != nil {
		return nil, ErrUnavailable
	} // LOCAL_MACHINE | UI_FORBIDDEN
	defer windows.LocalFree(windows.Handle(uintptr(unsafe.Pointer(output.Data))))
	if output.Size > 4<<20 {
		return nil, ErrUnavailable
	}
	return append([]byte(nil), unsafe.Slice(output.Data, int(output.Size))...), nil
}
func unseal(raw []byte) ([]byte, error) {
	input := blob(raw)
	var output windows.DataBlob
	if windows.CryptUnprotectData(&input, nil, nil, 0, nil, 1, &output) != nil {
		return nil, ErrUnavailable
	}
	defer windows.LocalFree(windows.Handle(uintptr(unsafe.Pointer(output.Data))))
	if output.Size > 2<<20 {
		return nil, ErrUnavailable
	}
	return append([]byte(nil), unsafe.Slice(output.Data, int(output.Size))...), nil
}
