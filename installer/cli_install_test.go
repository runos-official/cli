// Package installer holds the static CLI install scripts (cli-install.sh, cli-install.ps1) and
// their tests.
//
// The tests run the real shell script under bash with a PATH that holds only recording stubs,
// and assert that a hostile or malformed argument stops the script before ANY command runs.
package installer

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// stubbed is every external command the script could run. Each stub records its own name.
var stubbed = []string{
	"uname", "tr", "curl", "sed", "head", "mkdir", "mktemp", "rm", "sha256sum", "shasum", "awk",
	"gh", "grep", "tar", "chmod", "xattr", "cat", "bash", "sh", "runos",
}

func runInstaller(t *testing.T, env []string, args ...string) (int, string, []string) {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}
	dir := t.TempDir()
	stubs := filepath.Join(dir, "bin")
	if err := os.Mkdir(stubs, 0o755); err != nil {
		t.Fatal(err)
	}
	ran := filepath.Join(dir, "ran.log")
	for _, name := range stubbed {
		body := "#!" + bash + "\nprintf '%s\\n' " + name + " >> \"$RAN_LOG\"\nexit 1\n"
		if err := os.WriteFile(filepath.Join(stubs, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(bash, append([]string{"cli-install.sh"}, args...)...)
	cmd.Env = append([]string{"PATH=" + stubs, "RAN_LOG=" + ran, "HOME=" + dir}, env...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	err = cmd.Run()
	code := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	var names []string
	if raw, readErr := os.ReadFile(ran); readErr == nil {
		names = strings.Fields(string(raw))
	}
	return code, stderr.String(), names
}

var hostile = []string{
	"$(touch /tmp/pwned)",
	"`touch /tmp/pwned`",
	"v1.2.3; touch /tmp/pwned",
	"v1.2.3\ntouch /tmp/pwned",
	"v1.2.3' || touch /tmp/pwned #",
	"../../etc/passwd",
	"v1.2",
	"v1.2.3-",
	"latest",
	"\x1b[2J",
	"ｖ１.２.３",
}

// TestHostileReleaseRunsNothing is the security contract for the pin, in all three spellings.
func TestHostileReleaseRunsNothing(t *testing.T) {
	for _, value := range hostile {
		for name, call := range map[string]func() (int, string, []string){
			"--release":     func() (int, string, []string) { return runInstaller(t, nil, "--release", value) },
			"--release=":    func() (int, string, []string) { return runInstaller(t, nil, "--release="+value) },
			"RUNOS_VERSION": func() (int, string, []string) { return runInstaller(t, []string{"RUNOS_VERSION=" + value}) },
		} {
			code, stderr, ran := call()
			if code != 2 || len(ran) != 0 {
				t.Errorf("%s %q: exit %d and ran %v, want exit 2 and nothing run", name, value, code, ran)
			}
			if (strings.Contains(value, "pwned") || strings.Contains(value, "\x1b")) && strings.Contains(stderr, value) {
				t.Errorf("%s %q: the refusal echoed the value", name, value)
			}
		}
	}
}

func TestHostileInstallDirAndUnknownArgumentsRunNothing(t *testing.T) {
	cases := [][]string{
		{"--install-dir", "relative/dir"},
		{"--install-dir", "/tmp/x\ntouch /tmp/pwned"},
		{"--install-dir"},
		{"--release"},
		{"--unknown"},
		{"$(touch /tmp/pwned)"},
	}
	for _, args := range cases {
		code, _, ran := runInstaller(t, nil, args...)
		if code != 2 || len(ran) != 0 {
			t.Errorf("args %q: exit %d and ran %v, want exit 2 and nothing run", args, code, ran)
		}
	}
}

// TestValidPinPassesTheChecks is the positive control: a valid pin passes the checks, so the
// first command the script runs is uname (the OS detection after the checks).
func TestValidPinPassesTheChecks(t *testing.T) {
	for _, args := range [][]string{{"--release", "v1.22.3"}, {"--release=1.22.3-rc.1"}, {"--install-dir", "/tmp/runos-bin"}} {
		_, _, ran := runInstaller(t, nil, args...)
		// uname and tr run as one pipeline, so the order they are recorded in is not fixed.
		if !slices.Contains(ran, "uname") {
			t.Errorf("args %q: ran %v, want the install to reach uname", args, ran)
		}
	}
}

// TestTheScriptsAreStatic guards the design: the files are served unchanged, so they carry no
// template action for a server to fill in, and they keep the digest check.
func TestTheScriptsAreStatic(t *testing.T) {
	for _, name := range []string{"cli-install.sh", "cli-install.ps1"} {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		body := string(raw)
		for _, marker := range []string{"{{", "}}"} {
			if strings.Contains(body, marker) {
				t.Errorf("%s contains %q: the installer must be static", name, marker)
			}
		}
		if !strings.Contains(body, "https://runoscdn.com/cli/validated") {
			t.Errorf("%s must read the validated digest registry", name)
		}
	}
}

// fakeInstall runs the script against a fake world: real text tools, a curl that answers like
// the validated registry and fails the archive download, so the script stops right after it
// chose the exact release. It returns every URL curl was asked for.
func fakeInstall(t *testing.T, env []string, args ...string) (int, []string) {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}
	dir := t.TempDir()
	stubs := filepath.Join(dir, "bin")
	if err := os.Mkdir(stubs, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, real := range []string{"sed", "head", "tr", "grep", "awk", "cat", "rm", "mkdir", "mktemp"} {
		path, lookErr := exec.LookPath(real)
		if lookErr != nil {
			t.Skipf("%s is not installed", real)
		}
		if err := os.Symlink(path, filepath.Join(stubs, real)); err != nil {
			t.Fatal(err)
		}
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(stubs, name), []byte("#!"+bash+"\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("uname", `case "$1" in -s) echo Linux;; *) echo x86_64;; esac`+"\n")
	write("curl", `url=""
for a in "$@"; do case "$a" in -*) ;; *) url="$a";; esac; done
printf '%s\n' "$url" >> "$WORK/urls"
case "$url" in
  */current.json) echo '{"version":"v1.22.3","validated_at":"2026-10-24T07:21:16Z"}';;
  *.sha256) printf '%064d\n' 0;;
  *) exit 22;;
esac
`)
	cmd := exec.Command(bash, append([]string{"cli-install.sh"}, args...)...)
	cmd.Env = append([]string{"PATH=" + stubs, "WORK=" + dir, "HOME=" + dir, "TMPDIR=" + dir}, env...)
	err = cmd.Run()
	code := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "urls"))
	return code, strings.Fields(string(raw))
}

// TestAnExactReleaseCandidateIsInstalledExactly: a pinned hidden RC reads that release's own
// validated digest and downloads that exact archive, in every pin spelling.
func TestAnExactReleaseCandidateIsInstalledExactly(t *testing.T) {
	want := []string{
		"https://runoscdn.com/cli/validated/v1.22.3-rc.2/runos-linux-amd64.sha256",
		"https://github.com/runos-official/cli/releases/download/v1.22.3-rc.2/runos-linux-amd64.tar.gz",
	}
	for name, run := range map[string]func() (int, []string){
		"--release":     func() (int, []string) { return fakeInstall(t, nil, "--release", "v1.22.3-rc.2") },
		"bare version":  func() (int, []string) { return fakeInstall(t, nil, "--release=1.22.3-rc.2") },
		"RUNOS_VERSION": func() (int, []string) { return fakeInstall(t, []string{"RUNOS_VERSION=v1.22.3-rc.2"}) },
	} {
		code, urls := run()
		if code != 1 || strings.Join(urls, " ") != strings.Join(want, " ") {
			t.Errorf("%s: exit %d, fetched %v; want %v", name, code, urls, want)
		}
	}
}

// TestWithoutAPinTheValidatedCurrentReleaseIsInstalled: no pin, so the script reads the
// validated current.json pointer and installs exactly that release.
func TestWithoutAPinTheValidatedCurrentReleaseIsInstalled(t *testing.T) {
	code, urls := fakeInstall(t, nil)
	want := []string{
		"https://runoscdn.com/cli/validated/current.json",
		"https://runoscdn.com/cli/validated/v1.22.3/runos-linux-amd64.sha256",
		"https://github.com/runos-official/cli/releases/download/v1.22.3/runos-linux-amd64.tar.gz",
	}
	if code != 1 || strings.Join(urls, " ") != strings.Join(want, " ") {
		t.Fatalf("exit %d, fetched %v; want %v", code, urls, want)
	}
}
