//go:build windows

package vpn

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// The Windows service. `runos vpn install` (elevated) registers a service that runs
// `runos.exe vpn daemon` as LocalSystem, automatic start, restart on failure, and starts it. Same
// shape as launchd on macOS and systemd on Linux; the SCM plumbing is x/sys/windows/svc.
//
// wintun.dll must sit beside runos.exe (the release zip ships it, V8): wireguard-go's Windows tun
// loads it from the executable's directory. Install checks that before registering, so the
// failure is one sentence at install time rather than a service that dies on its first `up`.

const (
	serviceName        = "RunOSVPN"
	serviceDisplayName = "RunOS VPN"
	serviceDescription = "Connects this machine to RunOS clusters over the RunOS VPN (runos vpn up)."
	wintunDLL          = "wintun.dll"
)

// NewService returns the OS-specific VPN service installer.
func NewService() service { return windowsService{} }

type windowsService struct{}

func (windowsService) Describe() string {
	return "It runs in the background as the Windows service \"" + serviceDisplayName + "\" and starts at boot."
}

func (windowsService) Install(execPath, _ string, _ bool) error {
	if _, err := os.Stat(filepath.Join(filepath.Dir(execPath), wintunDLL)); err != nil {
		return fmt.Errorf("%s is missing beside %s: reinstall runos from the release zip, which ships it", wintunDLL, execPath)
	}
	if err := os.MkdirAll(StateDir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", StateDir, err)
	}
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to the service manager (elevated prompt?): %w", err)
	}
	defer m.Disconnect()

	// A re-install replaces the service so a moved binary or changed arguments take effect.
	if existing, err := m.OpenService(serviceName); err == nil {
		_ = stopService(existing)
		_ = existing.Delete()
		existing.Close()
		waitServiceGone(m)
	}
	s, err := m.CreateService(serviceName, execPath, mgr.Config{
		DisplayName:  serviceDisplayName,
		Description:  serviceDescription,
		StartType:    mgr.StartAutomatic,
		ErrorControl: mgr.ErrorNormal,
	}, "vpn", "daemon")
	if err != nil {
		return fmt.Errorf("create service: %w", err)
	}
	defer s.Close()
	// KeepAlive's equivalent: restart 5 s after a crash, every time.
	_ = s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
	}, 60)
	if err := s.Start(); err != nil {
		return fmt.Errorf("start service: %w", err)
	}
	return nil
}

func (windowsService) Uninstall() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to the service manager (elevated prompt?): %w", err)
	}
	defer m.Disconnect()
	s, err := m.OpenService(serviceName)
	if err == nil {
		_ = stopService(s)
		if err := s.Delete(); err != nil {
			s.Close()
			return fmt.Errorf("delete service: %w", err)
		}
		s.Close()
	}
	if err := os.Remove(SocketPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove socket: %w", err)
	}
	return nil
}

func (windowsService) Running() (bool, error) {
	m, err := mgr.Connect()
	if err != nil {
		return false, nil
	}
	defer m.Disconnect()
	s, err := m.OpenService(serviceName)
	if err != nil {
		return false, nil
	}
	defer s.Close()
	status, err := s.Query()
	if err != nil {
		return false, nil
	}
	return status.State == svc.Running, nil
}

func (windowsService) Restart() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to the service manager (run from an elevated prompt): %w", err)
	}
	defer m.Disconnect()
	s, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("open the %s service (is it installed? try `runos vpn install`): %w", serviceName, err)
	}
	defer s.Close()
	// A service already stopped is fine: the point is that the next start runs the current binary.
	_ = stopService(s)
	if err := s.Start(); err != nil {
		return fmt.Errorf("start the %s service: %w", serviceName, err)
	}
	return nil
}

// stopService asks the service to stop and waits (bounded) for it, so Delete and a following
// CreateService do not race a still-running instance.
func stopService(s *mgr.Service) error {
	status, err := s.Control(svc.Stop)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(20 * time.Second)
	for status.State != svc.Stopped && time.Now().Before(deadline) {
		time.Sleep(300 * time.Millisecond)
		if status, err = s.Query(); err != nil {
			return err
		}
	}
	return nil
}

// waitServiceGone waits (bounded) for the SCM to finish deleting a marked service; a CreateService
// while the old one is "marked for deletion" fails.
func waitServiceGone(m *mgr.Mgr) {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		s, err := m.OpenService(serviceName)
		if err != nil {
			return
		}
		s.Close()
		time.Sleep(300 * time.Millisecond)
	}
}
