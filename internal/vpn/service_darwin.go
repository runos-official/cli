//go:build darwin

package vpn

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// The macOS launchd LaunchDaemon. Written to /Library/LaunchDaemons so it runs at boot as root,
// KeepAlive so a crash restarts it, RunAtLoad so `install` starts it at once.

const (
	launchdLabel    = "com.runos.vpn"
	launchdPlistDir = "/Library/LaunchDaemons"
)

func launchdPlistPath() string { return launchdPlistDir + "/" + launchdLabel + ".plist" }

// NewService returns the OS-specific VPN service installer.
func NewService() service { return launchdService{} }

type launchdService struct{}

func (launchdService) Describe() string {
	return "It runs in the background as a launchd daemon (" + launchdLabel + ") and starts at boot."
}

func (s launchdService) Install(execPath, socketGroup string) error {
	plist := renderLaunchdPlist(execPath, socketGroup)
	if err := os.WriteFile(launchdPlistPath(), []byte(plist), 0o644); err != nil {
		return fmt.Errorf("write launchd plist (need sudo?): %w", err)
	}
	// bootout first so a re-install reloads cleanly; ignore its error (nothing loaded is fine).
	_, _ = run("launchctl", "bootout", "system/"+launchdLabel)
	if out, err := run("launchctl", "bootstrap", "system", launchdPlistPath()); err != nil {
		return fmt.Errorf("load launchd daemon: %w: %s", err, out)
	}
	return nil
}

func (s launchdService) Uninstall() error {
	_, _ = run("launchctl", "bootout", "system/"+launchdLabel)
	if err := os.Remove(launchdPlistPath()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove launchd plist: %w", err)
	}
	if err := os.Remove(SocketPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove socket: %w", err)
	}
	return nil
}

func (s launchdService) Restart() error {
	// kickstart -k kills and restarts in place, keeping the loaded definition; no plist rewrite.
	if out, err := run("launchctl", "kickstart", "-k", "system/"+launchdLabel); err != nil {
		return fmt.Errorf("restart launchd daemon (is it installed? try `sudo runos vpn install`): %w: %s", err, out)
	}
	return nil
}

func (s launchdService) Running() (bool, error) {
	out, err := exec.Command("launchctl", "print", "system/"+launchdLabel).CombinedOutput()
	if err != nil {
		// `launchctl print` exits non-zero when the label is not loaded; that is "not running".
		return false, nil
	}
	return strings.Contains(string(out), launchdLabel), nil
}

// renderLaunchdPlist builds the LaunchDaemon. The group owns the socket so the installing user's
// CLI reaches it without sudo; StandardError goes to a log the operator can read.
func renderLaunchdPlist(execPath, socketGroup string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>vpn</string>
		<string>daemon</string>
		<string>--socket-group</string>
		<string>%s</string>
	</array>
	<key>RunAtLoad</key><true/>
	<key>KeepAlive</key><true/>
	<key>StandardErrorPath</key><string>/var/log/runos-vpn.log</string>
	<key>StandardOutPath</key><string>/var/log/runos-vpn.log</string>
</dict>
</plist>
`, launchdLabel, execPath, socketGroup)
}
