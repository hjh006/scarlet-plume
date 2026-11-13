//go:build !windows

package endpointnative

import "errors"

var errWindowsOperation = errors.New("Windows endpoint executor unavailable on this operating system")

func CopyUSB(root, destination string, raw []byte) error   { return errWindowsOperation }
func WriteClipboard(raw []byte) error                      { return errWindowsOperation }
func PrintText(printer string, raw []byte) (string, error) { return "", errWindowsOperation }
