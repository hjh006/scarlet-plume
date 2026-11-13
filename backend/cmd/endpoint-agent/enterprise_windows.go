//go:build windows

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/endpointsecure"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

func enterpriseDirectory() (string, error) {
	p, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, 0)
	if err != nil {
		return "", errors.New("Windows program data directory unavailable")
	}
	return filepath.Join(p, "ScarletPlume", "EndpointNative"), nil
}
func enterprisePrivilege() string {
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err == nil && user.User.Sid.IsWellKnown(windows.WinLocalSystemSid) {
		return "system"
	}
	if token.IsElevated() {
		return "administrator"
	}
	return "not_elevated"
}

func installEnterpriseService(dir, expectedSHA256 string) error {
	if enterprisePrivilege() != "administrator" && enterprisePrivilege() != "system" {
		return errors.New("elevated administrator required to register service")
	}
	private, err := endpointsecure.Open(dir)
	if err != nil {
		return err
	}
	defer private.Close()
	cfg, err := loadEnterprise(private)
	if err != nil {
		return err
	}
	if _, err = cfg.client(); err != nil {
		return err
	}
	m, err := mgr.Connect()
	if err != nil {
		return errors.New("service manager unavailable")
	}
	defer m.Disconnect()
	if old, err := m.OpenService(endpointServiceName); err == nil {
		old.Close()
		return errors.New("service already exists; recover and remove it before replacing the installation")
	}
	source, err := os.Executable()
	if err != nil {
		return errors.New("agent executable unavailable")
	}
	target, err := private.InstallExecutable(source, expectedSHA256)
	if err != nil {
		return errors.New("approved executable hash mismatch, existing installation or protected copy unavailable; service not registered")
	}
	s, err := m.CreateService(endpointServiceName, target, mgr.Config{DisplayName: "Scarlet Plume Endpoint Native Controls", Description: "Approved bounded native configuration leases with protected recovery records; no user clipboard observation in SYSTEM session.", StartType: mgr.StartAutomatic, ErrorControl: mgr.ErrorNormal, DelayedAutoStart: true}, "-mode", "service-run")
	if err != nil {
		return errors.New("service registration failed; protected configuration retained")
	}
	return s.Close()
}
func removeEnterpriseService(dir string) error {
	m, err := mgr.Connect()
	if err != nil {
		return errors.New("service manager unavailable")
	}
	defer m.Disconnect()
	s, err := m.OpenService(endpointServiceName)
	if err != nil {
		return errors.New("service unavailable")
	}
	defer s.Close()
	status, err := s.Query()
	if err != nil || status.State != svc.Stopped {
		return errors.New("stop the service and verify rollback before removal")
	}
	if err := enterpriseNative(context.Background(), dir, "native-rollback"); err != nil {
		return err
	}
	// Keep the executable, DPAPI config, originals and receipt outbox. Deleting
	// evidence or bypassing a conflict is not part of service removal.
	if s.Delete() != nil {
		return errors.New("service removal failed; protected data retained")
	}
	return nil
}

type enterpriseService struct{ dir string }

func (s enterpriseService) Execute(_ []string, requests <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	changes <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- enterpriseNative(ctx, s.dir, "native-run") }()
	current := svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	changes <- current
	for {
		select {
		case err := <-done:
			if err != nil {
				return true, 1
			}
			return false, 0
		case request := <-requests:
			switch request.Cmd {
			case svc.Interrogate:
				changes <- current
			case svc.Stop, svc.Shutdown:
				changes <- svc.Status{State: svc.StopPending, WaitHint: 35000}
				cancel()
				select {
				case err := <-done:
					if err != nil {
						return true, 2
					}
					return false, 0
				case <-time.After(35 * time.Second):
					return true, 3
				}
			}
		}
	}
}
func runEnterpriseService(dir string) error {
	ok, err := svc.IsWindowsService()
	if err != nil || !ok {
		return errors.New("service-run must be launched by Windows Service Control Manager")
	}
	return svc.Run(endpointServiceName, enterpriseService{dir: dir})
}
