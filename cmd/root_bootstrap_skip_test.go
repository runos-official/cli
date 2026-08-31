package cmd

import "testing"

/*
Which `vpn` subcommands must skip the config and manifest bootstrap.

They are the ones that run AS ROOT and touch only the OS service. Root's home is /var/root, so a
config fetch there writes a stray /var/root/.runos that belongs to nobody, and a fetch that cannot
complete fails the command before it does its work.

`restart` was missing, and nothing asserted the set, so the omission shipped. MEASURED with 1.18.2:

	HOME=<empty dir> runos vpn restart     exit 1, and created <empty dir>/.runos
	HOME=<empty dir> runos vpn install     exit 1, created nothing

It matters more now that RunOS Desktop offers the restart behind an administrator prompt: the
command then runs as root, so every restart left a root-owned config behind, and an unreachable
config endpoint failed the bootstrap BEFORE the restart ran, taking a password and doing nothing.
*/
func TestTheRootOnlyVPNCommandsSkipTheBootstrap(t *testing.T) {
	for _, name := range []string{"daemon", "install", "uninstall", "restart"} {
		if !isRootOnlyVPNCommand(name) {
			t.Errorf("vpn %s runs as root and touches only the service, so it must skip the bootstrap", name)
		}
	}
	// Everything else needs a credential and a manifest, and runs as the person.
	for _, name := range []string{"up", "down", "status", "connect", "disconnect", "forget-key", "devices"} {
		if isRootOnlyVPNCommand(name) {
			t.Errorf("vpn %s needs the bootstrap: it speaks to conductor as the signed-in person", name)
		}
	}
}
