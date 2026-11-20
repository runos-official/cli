package update

// SetReleaseForTest points the downloads at a fake release server and the replaced binary at a
// scratch file, and restores both when the test ends. Exported because the `cmd` package's update
// tests need it; it has no other caller.
func SetReleaseForTest(t interface{ Cleanup(func()) }, releaseBaseURL, executable string) {
	previousBase, previousExe := cdnBaseURL, executablePath
	cdnBaseURL = releaseBaseURL
	executablePath = func() (string, error) { return executable, nil }
	t.Cleanup(func() { cdnBaseURL, executablePath = previousBase, previousExe })
}
