//go:build darwin

package vpn

import (
	"strings"
	"testing"
)

// The plist must not hand launchd an unbounded file. The daemon writes its own bounded log, so
// launchd's streams go nowhere.
func TestLaunchdPlistHandsNoUnboundedFileToLaunchd(t *testing.T) {
	plist := renderLaunchdPlist("/usr/local/bin/runos", "admin", false)
	if strings.Contains(plist, "/var/log/runos-vpn.log") {
		t.Error("the plist still names the unbounded launchd log file")
	}
	for _, key := range []string{"StandardErrorPath", "StandardOutPath"} {
		if !strings.Contains(plist, "<key>"+key+"</key><string>/dev/null</string>") {
			t.Errorf("%s is not /dev/null", key)
		}
	}
}
