package cmd

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/runos-official/cli/internal/auth"
	"github.com/runos-official/cli/internal/config"
)

// Everything the cursor target puts on the user's screen lives here: the
// warning that runs before anything is written, and the report that runs after.
// The two have to agree with each other and with cursorMCPServers, so they sit
// side by side.

func reportCursorResult(opts cursorOptions, changed int, guardRuns bool) {
	fmt.Fprintln(opts.out)
	if changed == 0 {
		fmt.Fprintln(opts.out, "Already up to date. Nothing changed.")
	} else {
		fmt.Fprintf(opts.out, "Done. %s brought up to date.\n", cursorFileCount(changed))
	}

	// The sign-in state is checked last because this is where the user reads
	// it. The version before this told an unauthenticated user that Cursor had
	// access to the tools, and every call then came back not authenticated.
	if cursorSignedIn() {
		fmt.Fprintln(opts.out, "Cursor has access to the RunOS tools in this project.")
	} else {
		fmt.Fprintln(opts.out, "You are not signed in. Run `runos login` before you use the tools in Cursor.")
	}

	if !guardRuns {
		fmt.Fprintln(opts.out)
		fmt.Fprintln(opts.out, "Only the read server is declared, and no guard hook was written:")
		fmt.Fprintf(opts.out, "the guard is a bash script and %s cannot run it.\n", cursorGOOS)
		return
	}

	fmt.Fprintln(opts.out)
	fmt.Fprintln(opts.out, "The guard hook asks you before every call to a server that can change")
	fmt.Fprintln(opts.out, "or reveal something. It is registered failClosed, so a guard that cannot")
	fmt.Fprintln(opts.out, "run blocks the call instead of letting it through.")
}

func cursorFileCount(changed int) string {
	if changed == 1 {
		return "1 file"
	}
	return fmt.Sprintf("%d files", changed)
}

// confirmCursorWrite is the one place the user is told what is about to load,
// before anything is written. It mirrors the codex target, which is the repo's
// precedent for a client that cannot be relied on to ask by itself.
func confirmCursorWrite(opts cursorOptions) (bool, error) {
	projectPath, err := os.Getwd()
	if err != nil {
		return false, fmt.Errorf("failed to get current directory: %w", err)
	}

	fmt.Fprintln(opts.out, "╔════════════════════════════════════════════════════════════════════════════╗")
	fmt.Fprintln(opts.out, "║                              ⚠️  WARNING ⚠️                                 ║")
	fmt.Fprintln(opts.out, "╚════════════════════════════════════════════════════════════════════════════╝")
	fmt.Fprintln(opts.out)
	fmt.Fprintln(opts.out, "This declares four RunOS MCP servers in this project, and Cursor loads all")
	fmt.Fprintln(opts.out, "four. There is no per-server switch in .cursor/mcp.json.")
	fmt.Fprintln(opts.out)
	for _, server := range cursorMCPServers {
		risk := "performs no mutation"
		if server.risk != "" {
			risk = server.risk
		}
		fmt.Fprintf(opts.out, "  %-22s %s\n", server.name, risk)
	}
	fmt.Fprintln(opts.out)
	// "Performs no mutation" is the whole of what the read tier promises, and
	// saying more than that would be false today.
	fmt.Fprintln(opts.out, "The read server changes nothing. It is not free of secrets: on a CLI older")
	fmt.Fprintln(opts.out, "than manifest 45.0.0, five services return credentials from the read tier.")
	fmt.Fprintln(opts.out)
	fmt.Fprintln(opts.out, "A guard hook asks you before every call to the last three. Two things to")
	fmt.Fprintln(opts.out, "know about it:")
	fmt.Fprintln(opts.out)
	fmt.Fprintln(opts.out, "  • Hooks are a Cursor editor feature. A client that does not read")
	fmt.Fprintln(opts.out, "    .cursor/hooks.json runs no guard at all, and cursor-agent 2025.09.17")
	fmt.Fprintln(opts.out, "    is such a client. There the approvals prompt is the only brake.")
	fmt.Fprintln(opts.out, "  • It is registered failClosed, so a guard that cannot run blocks every")
	fmt.Fprintln(opts.out, "    MCP call in this project, including another tool's servers.")
	fmt.Fprintln(opts.out)
	fmt.Fprintln(opts.out, "Run `runos mcp configure cursor --read-only` to declare the read server")
	fmt.Fprintln(opts.out, "alone. That is the only way to stop the other three loading.")
	fmt.Fprintln(opts.out)
	fmt.Fprintf(opts.out, "Project path: %s\n", projectPath)
	fmt.Fprintln(opts.out)
	fmt.Fprint(opts.out, "Type 'yes' to proceed: ")

	response, err := bufio.NewReader(opts.in).ReadString('\n')
	// A closed stdin reads EOF with no newline. Whatever arrived is judged on
	// its own, so a piped "yes" with no trailing newline still proceeds.
	if err != nil && err != io.EOF {
		return false, fmt.Errorf("failed to read response: %w", err)
	}
	if strings.TrimSpace(strings.ToLower(response)) != "yes" {
		return false, nil
	}
	fmt.Fprintln(opts.out)
	return true, nil
}

// cursorSignedIn reports whether a credential is on hand. It asks UsingAPIKey
// first so the no-config path never reaches config.Load's CDN fetch, which
// would put a network call inside a command that writes local files.
func cursorSignedIn() bool {
	if auth.UsingAPIKey() {
		return true
	}
	cfg, err := config.Load()
	if err != nil {
		return false
	}
	return auth.HasCredentials(cfg)
}
