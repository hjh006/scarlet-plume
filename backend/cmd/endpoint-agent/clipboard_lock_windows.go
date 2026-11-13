//go:build windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

func clipboardPrivateACL(path string) bool {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return false
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return false
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !owner.Equals(user.User.Sid) {
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
		if !sid.Equals(user.User.Sid) && !sid.IsWellKnown(windows.WinLocalSystemSid) {
			return false
		}
	}
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false
	}
	flags, err := windows.GetFileAttributes(p)
	return err == nil && flags&windows.FILE_ATTRIBUTE_REPARSE_POINT == 0
}
func lockClipboardSession(root *os.Root) (func(), error) {
	if !clipboardPrivateACL(root.Name()) {
		return nil, errors.New("clipboard state must be private to this user and SYSTEM")
	}
	f, err := root.OpenFile("runtime.lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, errors.New("clipboard runtime lock unavailable")
	}
	path := filepath.Join(root.Name(), "runtime.lock")
	var info windows.ByHandleFileInformation
	if !clipboardPrivateACL(path) || windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &info) != nil || info.NumberOfLinks != 1 {
		f.Close()
		return nil, errors.New("clipboard runtime lock owner or link invalid")
	}
	var overlapped windows.Overlapped
	if windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlapped) != nil {
		f.Close()
		return nil, errors.New("another clipboard guard already owns this user runtime")
	}
	return func() { _ = f.Close() }, nil
}
