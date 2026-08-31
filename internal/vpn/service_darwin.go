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

func (s launchdService) Install(execPath, socketGroup string, groupExplicit bool) error {
	plist := renderLaunchdPlist(execPath, socketGroup, groupExplicit)
	if err := os.WriteFile(launchdPlistPath(), []byte(plist), 0o644); err != nil {
		return fmt.Errorf("write launchd plist (need sudo?): %w", err)
	}
	// bootout first so a re-install reloads cleanly; ignore its error (nothing loaded is fine).
	_, _ = run("launchctl", "bootout", "system/"+launchdLabel)

	// THEN ENABLE THE LABEL, and this line is the whole of a defect that cost a real afternoon.
	//
	// launchd keeps a persistent DISABLED set per domain, and `bootstrap` refuses a disabled label
	// with `Bootstrap failed: 5: Input/output error`. That message names nothing: not the label,
	// not the disabled state, not the remedy. Measured on the operator's Mac 2026-08-20, on a
	// machine where the daemon had run happily on 16 August. The label was not loaded, the plist
	// linted clean, the binary existed and was correctly ad-hoc signed, and nothing was
	// quarantined, so every obvious cause read as fine while install kept failing.
	//
	// WORSE, `launchctl print-disabled system` did NOT list it. So the state that blocks bootstrap
	// is not reliably visible even when you know to look for it. `launchctl enable` cleared it and
	// the install then succeeded first time.
	//
	// Enable is idempotent and harmless on a label that was never disabled, so it is unconditional
	// rather than conditional on a query that has just been shown not to answer.
	_, _ = run("launchctl", "enable", "system/"+launchdLabel)

	if out, err := run("launchctl", "bootstrap", "system", launchdPlistPath()); err != nil {
		// AND IF IT STILL FAILS, say something the operator can act on rather than passing
		// launchd's errno through. "Input/output error" sent two people down the wrong path.
		return fmt.Errorf(
			"load launchd daemon %s: %w: %s\n"+
				"  The plist is at %s and was written successfully, so this is launchd refusing to load it.\n"+
				"  Most often that is stale launchd state. Try:\n"+
				"    sudo launchctl bootout system/%s ; sudo launchctl enable system/%s ; sudo %s\n"+
				"  If it still refuses, a reboot clears launchd state that survives everything else.",
			launchdLabel, err, out, launchdPlistPath(), launchdLabel, launchdLabel, "runos vpn install")
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
func renderLaunchdPlist(execPath, socketGroup string, groupExplicit bool) string {
	source := ""
	if groupExplicit {
		// Recorded so the daemon leaves this group alone. Absent means the installer derived it,
		// which is what every machine installed by an older build looks like, so those still heal.
		source = "\n\t\t<string>--socket-group-source</string>\n\t\t<string>explicit</string>"
	}
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
		<string>%s</string>%s
	</array>
	<key>RunAtLoad</key><true/>
	<key>KeepAlive</key><true/>
	<key>StandardErrorPath</key><string>/var/log/runos-vpn.log</string>
	<key>StandardOutPath</key><string>/var/log/runos-vpn.log</string>
</dict>
</plist>
`, launchdLabel, execPath, socketGroup, source)
}
