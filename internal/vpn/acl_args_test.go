package vpn

import (
	"strings"
	"testing"
)

// Unverified on a real Windows host (there is none in the test fleet): these pin the rules the
// icacls arguments express, so a later edit cannot quietly open the key file to Users.
func TestStateDirIsClosedToEveryoneButSystemAndAdministrators(t *testing.T) {
	args := strings.Join(stateDirArgs(`C:\ProgramData\RunOS\vpn`), " ")
	for _, want := range []string{"/inheritance:r", sidSystem + ":(OI)(CI)F", sidAdministrators + ":(OI)(CI)F"} {
		if !strings.Contains(args, want) {
			t.Errorf("directory rules lack %q: %s", want, args)
		}
	}
	// Users: traverse and list the directory only. No inheritance flag, so no file inherits it.
	if !strings.Contains(args, sidUsers+":(RX)") || strings.Contains(args, sidUsers+":(OI)") || strings.Contains(args, sidUsers+":(CI)") {
		t.Errorf("Users must get read/traverse on the directory itself and nothing inherited: %s", args)
	}
	if strings.Contains(args, sidUsers+":(M)") || strings.Contains(args, sidUsers+":F") || strings.Contains(args, sidUsers+":(OI)(CI)F") {
		t.Errorf("Users must never get write or full control: %s", args)
	}
}

func TestThePrivateKeyFileIsNeverGrantedToUsers(t *testing.T) {
	args := strings.Join(privateFileArgs(`C:\ProgramData\RunOS\vpn\state.json`), " ")
	if strings.Contains(args, sidUsers) || strings.Contains(args, "Users") || strings.Contains(args, "Everyone") {
		t.Errorf("the key file must not be granted to Users or Everyone: %s", args)
	}
	for _, want := range []string{"/inheritance:r", sidSystem + ":F", sidAdministrators + ":F"} {
		if !strings.Contains(args, want) {
			t.Errorf("key file rules lack %q: %s", want, args)
		}
	}
}

func TestTheLogIsGrantedToUsersAsReadOnly(t *testing.T) {
	args := logReadArgs(`C:\ProgramData\RunOS\vpn\daemon.log`)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, sidUsers+":(R)") || strings.Contains(joined, "/inheritance") {
		t.Errorf("want a plain read grant that leaves the file's other rules alone: %s", joined)
	}
}
