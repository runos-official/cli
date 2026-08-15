//go:build linux

package vpn

import (
	"fmt"
	"os"
	"strings"
)

// The Linux systemd service. `runos vpn install` writes a unit that runs `runos vpn daemon` as
// root and enables it. Same shape as the launchd daemon on macOS.

const systemdUnitPath = "/etc/systemd/system/runos-vpn.service"

// NewService returns the OS-specific VPN service installer.
func NewService() service { return systemdService{} }

type systemdService struct{}

func (systemdService) Describe(execPath string) string {
	return fmt.Sprintf("a systemd service 'runos-vpn' at %s running %q, plus the socket at %s (remove with 'sudo runos vpn uninstall')",
		systemdUnitPath, execPath+" vpn daemon", SocketPath)
}

func (s systemdService) Install(execPath, socketGroup string) error {
	unit := renderSystemdUnit(execPath, socketGroup)
	if err := os.WriteFile(systemdUnitPath, []byte(unit), 0o644); err != nil {
		return fmt.Errorf("write systemd unit (need sudo?): %w", err)
	}
	if out, err := run("systemctl", "daemon-reload"); err != nil {
		return fmt.Errorf("systemctl daemon-reload: %w: %s", err, out)
	}
	if out, err := run("systemctl", "enable", "--now", "runos-vpn.service"); err != nil {
		return fmt.Errorf("enable runos-vpn: %w: %s", err, out)
	}
	return nil
}

func (s systemdService) Uninstall() error {
	_, _ = run("systemctl", "disable", "--now", "runos-vpn.service")
	if err := os.Remove(systemdUnitPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove systemd unit: %w", err)
	}
	_, _ = run("systemctl", "daemon-reload")
	if err := os.Remove(SocketPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove socket: %w", err)
	}
	return nil
}

func (s systemdService) Running() (bool, error) {
	out, _ := run("systemctl", "is-active", "runos-vpn.service")
	return strings.TrimSpace(string(out)) == "active", nil
}

// renderSystemdUnit builds the service unit. The socket group lets the installing user's CLI
// reach the socket without sudo.
func renderSystemdUnit(execPath, socketGroup string) string {
	return fmt.Sprintf(`[Unit]
Description=RunOS VPN daemon
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s vpn daemon --socket-group %s
Restart=on-failure
RestartSec=2

[Install]
WantedBy=multi-user.target
`, execPath, socketGroup)
}
