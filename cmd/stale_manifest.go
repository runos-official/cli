package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/manifest"
)

// Recovering from a command that is missing only because the cached command list is old
// (goal 21, O10).
//
// THE DEFECT. Nearly every RunOS command is built from a manifest the CLI caches in
// ~/.runos/manifest.json. Ship a new command in conductor and the CLI keeps answering
// `unknown command "virt" for "runos"` until that cache is refreshed. Nothing in the error,
// the help, or `config get` mentions a cache.
//
// WHY IT MATTERS MORE TO AN AGENT THAN TO A PERSON. The failure is indistinguishable from
// "the feature was not deployed". An agent that has just shipped a command and is told it
// does not exist will reasonably conclude its own deploy failed and go and debug the API,
// which is the wrong place and the expensive one. It also quietly defeats the rule this goal
// exists to enforce: an agent that cannot see a command reaches for the raw API instead, and
// the gap never gets logged, because from where it stands the command simply is not there.
//
// Recorded as hit EIGHT times before this fix, and twice more on 2026-08-13. Every time it
// cost nothing only because a previous session had written the trap into the handoff. That is
// not a fix, it is an agent carrying a workaround.
//
// THE MECHANISM ALREADY EXISTED. `runos manifest update` has always worked. What was missing
// was discovery: an agent whose symptom is `unknown command` has no reason to open
// `runos manifest`. So this closes the loop rather than adding a capability.

// staleManifestVerdict is what to do about an unknown command, decided from the two versions.
type staleManifestVerdict int

const (
	// The cached list matches the server, so the command genuinely does not exist.
	verdictCommandUnknown staleManifestVerdict = iota
	// The cached list is behind the server, so the command may exist and the cache is at fault.
	verdictCacheStale
	// The server could not be asked, so neither can be ruled out.
	verdictCannotTell
)

// judgeStaleManifest decides why a command was not found.
//
// Kept pure and separate from the I/O so the decision is testable. `serverErr` non-nil means
// the version endpoint could not be reached, which is deliberately NOT treated as "stale":
// telling an offline user their cache is out of date would be a guess.
func judgeStaleManifest(cachedVersion, serverVersion string, serverErr error) staleManifestVerdict {
	if serverErr != nil || serverVersion == "" {
		return verdictCannotTell
	}
	if cachedVersion == serverVersion {
		return verdictCommandUnknown
	}
	return verdictCacheStale
}

// isUnknownCommandError reports whether cobra failed because it did not recognise the command.
//
// Matched on the message because cobra returns a plain fmt.Errorf here with no typed error to
// check. Deliberately narrow: an unrecognised shape is treated as an ordinary error, because
// re-fetching the manifest in response to an unrelated failure would be worse than useless.
func isUnknownCommandError(err error) bool {
	return unknownSubject(err) != ""
}

// unknownSubject reports WHAT cobra did not recognise: "command",
// "flag", or "" for any other error.
//
// The two need different wording. A stale command list explains a missing
// command; it explains a missing flag too, since a flag arrives with the
// command it belongs to. But telling an operator "this command really
// does not exist" when the command exists and only the flag was wrong
// sends them to look for a failed deploy. Regression target: goal 21 B6.
func unknownSubject(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	switch {
	case strings.HasPrefix(msg, "unknown command"):
		return "command"
	case strings.HasPrefix(msg, "unknown flag"), strings.HasPrefix(msg, "unknown shorthand flag"):
		return "flag"
	}
	return ""
}

// newManifestLoader builds a loader against the configured API, as the manifest command does.
func newManifestLoader() (*manifest.Loader, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return manifest.NewLoader(cfg.GetAPIURL(), filepath.Join(home, ".runos")), nil
}

// explainPossiblyStaleManifest prints guidance after an unknown-command failure, and refreshes
// the cache when it is genuinely behind.
//
// It writes to stderr and never changes the exit code: the command still failed, and a CI gate
// keying off the status must keep tripping. It also does NOT retry the command, because the
// cobra tree was built at process start and rebuilding it mid-flight would run init twice.
// Telling the operator to re-run is honest and costs one command.
func explainPossiblyStaleManifest(err error) {
	if !isUnknownCommandError(err) {
		return
	}

	loader, lerr := newManifestLoader()
	if lerr != nil {
		return
	}

	cached := ""
	if m, merr := loader.LoadLocal(); merr == nil && m != nil {
		cached = m.Version
	}

	server, serr := loader.ServerVersion()

	subject := unknownSubject(err)
	switch judgeStaleManifest(cached, server, serr) {
	case verdictCacheStale:
		fmt.Fprintf(os.Stderr,
			"\nYour cached command list is out of date (it has %s, the server is serving %s).\n"+
				"That is very likely why this %s was not found.\n", cached, server, subject)
		if _, uerr := loader.ForceUpdate(); uerr != nil {
			fmt.Fprintf(os.Stderr, "Run `runos manifest update` to refresh it, then try again.\n")
			return
		}
		fmt.Fprintf(os.Stderr, "RunOS has refreshed it to %s. Run your command again.\n", server)
	case verdictCommandUnknown:
		if subject == "flag" {
			fmt.Fprintf(os.Stderr,
				"\nYour cached command list is current (%s), so this flag really does not exist on this command.\n"+
					"The COMMAND is fine; run it with --help to see the flags it does take.\n", cached)
			return
		}
		fmt.Fprintf(os.Stderr,
			"\nYour cached command list is current (%s), so this command really does not exist.\n"+
				"Run `runos --help` to see what does.\n", cached)
	case verdictCannotTell:
		fmt.Fprintf(os.Stderr,
			"\nRunOS could not reach the API to check whether your cached command list is current.\n"+
				"If you expected this %s to exist, run `runos manifest update` once you are online.\n", subject)
	}
}
