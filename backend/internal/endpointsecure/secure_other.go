//go:build !windows

package endpointsecure

import "os"

func secureDirectory(string) error                         { return ErrUnavailable }
func seal([]byte) ([]byte, error)                          { return nil, ErrUnavailable }
func unseal([]byte) ([]byte, error)                        { return nil, ErrUnavailable }
func secureExistingFile(*os.File) error                    { return ErrUnavailable }
func secureCreatedFile(*os.File, string) error             { return ErrUnavailable }
func acquireStoreLock(*os.Root) (*os.File, error)          { return nil, ErrUnavailable }
func createPrivateFile(*os.Root, string) (*os.File, error) { return nil, ErrUnavailable }
func commitExecutable(string, string) error                { return ErrUnavailable }
