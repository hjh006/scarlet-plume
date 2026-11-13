//go:build !windows

package main

import "errors"

func enterpriseDirectory() (string, error) {
	return "", errors.New("Windows enterprise installation unavailable on this OS")
}
func enterprisePrivilege() string                   { return "unknown" }
func installEnterpriseService(string, string) error { return errors.New("Windows service unavailable") }
func removeEnterpriseService(string) error          { return errors.New("Windows service unavailable") }
func runEnterpriseService(string) error             { return errors.New("Windows service unavailable") }
