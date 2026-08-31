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

func (systemdService) Describe() string {
	return "It runs in the background as the systemd service runos-vpn and starts at boot."
}

func (s systemdService) Install(execPath, socketGroup string, groupExplicit bool) error {
	unit := renderSystemdUnit(execPath, socketGroup, groupExplicit)
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

func (s systemdService) Restart() error {
	if out, err := run("systemctl", "restart", "runos-vpn.service"); err != nil {
		return fmt.Errorf("restart runos-vpn (is it installed? try `sudo runos vpn install`): %w: %s", err, out)
	}
	return nil
}

func (s systemdService) Running() (bool, error) {
	out, _ := run("systemctl", "is-active", "runos-vpn.service")
	return strings.TrimSpace(string(out)) == "active", nil
}

// renderSystemdUnit builds the service unit. The socket group lets the installing user's CLI
// reach the socket without sudo.
/*
renderSystemdUnit writes the unit.

THE ARGUMENTS ARE BUILT, NOT INTERPOLATED. `--socket-group %s` with an empty group emitted a flag
with nothing after it; systemd splits on whitespace, so argv ended at `--socket-group`, cobra
answered "flag needs an argument", and the daemon exited before it ran a line of its own code.
Restart=on-failure then looped forever while `systemctl enable --now` reported success, because a
Type=simple unit succeeds as soon as exec does. The install printed "RunOS VPN service installed."
over a service that never came up, with nothing on stdout or stderr to say why.

Reachable on a machine with neither a `sudo` nor a `wheel` group, installed from a root shell:
openSUSE is the case that has neither, and `su -` is its usual admin flow.
*/
func renderSystemdUnit(execPath, socketGroup string, groupExplicit bool) string {
	args := execPath + " vpn daemon"
	if socketGroup != "" {
		args += " --socket-group " + socketGroup
		if groupExplicit {
			args += " --socket-group-source explicit"
		}
	}
	return fmt.Sprintf(`[Unit]
Description=RunOS VPN daemon
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s
Restart=on-failure
RestartSec=2

[Install]
WantedBy=multi-user.target
`, args)
}
