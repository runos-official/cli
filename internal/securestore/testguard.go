package securestore

import (
	"os"
	"strings"

	"github.com/runos-official/cli/internal/testguard"
)

/*
A test binary never reaches the real OS credential store.

The person's sign-in lives in that store under the CLI's own service name. A test that opened it and
then signed out deleted the developer's real sign-in. So in a test binary Open treats the OS store as
absent, and an encrypted file is allowed only under the temp directory. A test that needs a store
passes Options.Platform with a fake, or sets RUNOS_CREDENTIAL_PASSPHRASE with a temp HOME.

The one exception is this package's own keychain tests. They run only with RUNOS_TEST_KEYCHAIN=1, and
only under a throwaway service name that starts with testServicePrefix, never the CLI's real one.
*/

// testServicePrefix marks a throwaway service name that a keychain test may create and delete.
const testServicePrefix = "runos-cli-test-"

// realStoreAllowed reports whether this process may touch the OS store under service.
func realStoreAllowed(service string) bool {
	if !testguard.Active() {
		return true
	}
	return os.Getenv("RUNOS_TEST_KEYCHAIN") == "1" && strings.HasPrefix(service, testServicePrefix)
}

// encryptedFileAllowed reports whether this process may open an encrypted file at path.
func encryptedFileAllowed(path string) bool {
	return !testguard.Active() || testguard.UnderTempDir(path)
}

// errRealStoreInTest is the refusal a test binary gets instead of the real store.
func errRealStoreInTest(reason string) error {
	return &NoSecureStoreError{Reason: "test binary: " + reason +
		"; pass Options.Platform with a fake store, or set RUNOS_CREDENTIAL_PASSPHRASE with a temp HOME"}
}

// absentInTest is the OS store lookup a test binary gets: there is none.
func absentInTest(string) (Store, bool) { return nil, false }
