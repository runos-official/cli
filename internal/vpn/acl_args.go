package vpn

/*
The Windows access rules for the daemon's state directory, as the icacls arguments that set them.

WHY THERE ARE RULES AT ALL. On Windows the state file (state.json, which holds the device's PRIVATE
key) and the daemon log share one directory under ProgramData, and a directory created there
inherits "Users may read" from its parent. os.MkdirAll(dir, 0o700) means nothing on Windows, and the
only ACL code was for the socket file. So any local user could read the private key.

THE RULES. SYSTEM and Administrators get full control of the directory and, by inheritance, of every
file the daemon creates in it (the key file among them). Users get read and traverse on the
directory ITSELF only, with no inheritance to its files: they can reach the socket and the log
through it, and the two files that are meant for them carry their own explicit grant.

Pure so it is tested on every OS; the Windows files run it. The SIDs are well known and
locale-independent, which a name like "Administrators" is not.
*/

const (
	sidSystem         = "*S-1-5-18"
	sidAdministrators = "*S-1-5-32-544"
	sidUsers          = "*S-1-5-32-545"
)

// stateDirArgs closes the directory to everyone but SYSTEM and Administrators, and lets Users
// traverse and list it (not read what is in it).
func stateDirArgs(dir string) []string {
	return []string{
		dir, "/inheritance:r",
		"/grant:r", sidSystem + ":(OI)(CI)F",
		"/grant:r", sidAdministrators + ":(OI)(CI)F",
		"/grant:r", sidUsers + ":(RX)",
	}
}

// privateFileArgs closes one existing file to everyone but SYSTEM and Administrators. It brings a
// state file written by an earlier build, which inherited Users read, into line.
func privateFileArgs(path string) []string {
	return []string{
		path, "/inheritance:r",
		"/grant:r", sidSystem + ":F",
		"/grant:r", sidAdministrators + ":F",
	}
}

// logReadArgs lets Users read the daemon log, the same audience the control socket is opened to.
func logReadArgs(path string) []string {
	return []string{path, "/grant", sidUsers + ":(R)"}
}
