package mcp

// The gateway: a handful of tools instead of one per command.
//
// WHY THIS EXISTS. The per-command surface does not scale. Measured on CLI
// 1.19.1: 693 distinct tools across four servers, about 826 KB of definitions,
// which exceeds a 200k context window before a single user message. RunOS
// expects to pass 1,000 commands, and one tool per command grows linearly with
// that. See FPL30.
//
// THE PERMISSION RULE IS AN ALLOW-LIST DERIVED FROM THE MANIFEST, and that
// choice is load-bearing. A deny-list of "dangerous" commands fails OPEN: it
// goes stale the moment someone adds a cobra command, and the person adding it
// has no reason to think about this file. An allow-list fails CLOSED, so a new
// command is unreachable until someone deliberately admits it.
//
// The escalation this prevents is real and specific. `config` IS a manifest
// group, but only `config/get` is in the manifest; `config set` and
// `config env` are cobra-only. Matching on the GROUP would admit
// `runos config set api-url <attacker>`, repointing the CLI at another
// conductor in one call. Matching on the EXACT command path does not.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/runos-official/cli/internal/dynacmd"
	"github.com/runos-official/cli/internal/manifest"
)

// Mode is the gateway's read/write posture. It is set from argv at spawn and
// is immutable for the life of the process.
//
// IT MUST NOT COME FROM CONFIG. `runos config set` is a runos command, so a
// config-scoped mode is one the model can flip after a refusal and retry.
// Reading it from argv means nothing the model can run changes it.
type Mode string

const (
	ModeRO Mode = "ro"
	ModeRW Mode = "rw"
)

// staticAllow are commands with NO manifest entry that an agent still needs.
// Everything absent from both this list and the manifest is refused.
//
// Keep this list short and justify each entry. It is the only hand-maintained
// part of the permission rule, and the only place a mistake fails open.
var staticAllow = map[string]Mode{
	"status":        ModeRO, // is the CLI signed in, which environment
	"version":       ModeRO, // the running binary's version
	"version-check": ModeRO, // is an update available
	"follow":        ModeRO, // watch a job the agent already started
}

// staticDenyReason explains the refusal for commands an agent may reach for.
// A refusal that names the reason stops the agent retrying variations of it.
var staticDenyReason = map[string]string{
	"login":      "sign-in is the user's action, not the agent's. Ask them to run `runos login` in their own terminal.",
	"logout":     "signing the user out is never the agent's call.",
	"config":     "only `config get` is exposed. Changing CLI configuration, in particular `config set api-url`, would repoint this CLI at a different conductor.",
	"update":     "updating the binary mid-session changes the tool surface underneath the running server.",
	"shell":      "opens an interactive session, which has no meaning over a tool call.",
	"desktop":    "manages a GUI application on the user's machine.",
	"manifest":   "rewrites the command surface this gateway derives its permissions from.",
	"procedures": "carries its own approval model; drive it directly rather than through the gateway.",
	"mcp":        "configures or serves MCP itself. Reconfiguring the server from inside the server is a loop.",
	"completion": "emits shell completion scripts, of no use to an agent.",
	"help":       "use runos_catalog instead, which is built for this.",
}

// commandIndex maps an exact manifest command path to its definition.
type commandIndex map[string]manifest.Command

func buildIndex(m *manifest.Manifest) commandIndex {
	idx := make(commandIndex, len(m.Commands))
	for _, c := range m.Commands {
		idx[c.Command] = c
	}
	return idx
}

// resolveCommand finds the manifest command an argv refers to.
//
// It takes the LONGEST matching prefix of non-flag tokens, because a command
// path and a positional argument are indistinguishable by shape:
// ["vms","show","abc123"] is the command `vms/show` with a positional, while
// ["services","valkey","add"] is the three-segment command `services/valkey/add`.
// Longest-match resolves both without guessing.
func (idx commandIndex) resolveCommand(argv []string) (manifest.Command, string, bool) {
	var path []string
	for _, a := range argv {
		if strings.HasPrefix(a, "-") {
			break
		}
		path = append(path, a)
	}
	for n := len(path); n > 0; n-- {
		key := strings.Join(path[:n], "/")
		if c, ok := idx[key]; ok {
			return c, key, true
		}
	}
	return manifest.Command{}, strings.Join(path, "/"), false
}

// permits reports whether the mode may run this command, and why not.
func permits(c manifest.Command, mode Mode) (bool, string) {
	write := false
	read := false
	for _, tier := range c.MCP {
		switch tier {
		case "write", "sensitive_write":
			write = true
		case "read", "sensitive_read":
			read = true
		}
	}
	if len(c.MCP) == 0 {
		// No declared tier. Fall back to the method: a GET changes nothing.
		if strings.EqualFold(c.Method, "GET") {
			read = true
		} else {
			write = true
		}
	}
	if mode == ModeRW {
		return true, ""
	}
	if write {
		return false, fmt.Sprintf(
			"`%s` writes, and this server is running read-only. Ask the user to enable the runos-rw server, then try again.",
			strings.ReplaceAll(c.Command, "/", " "))
	}
	if !read {
		return false, fmt.Sprintf("`%s` is not a read command and this server is read-only.", c.Command)
	}
	return true, ""
}

// Refusal is a denial with a reason the agent can act on.
type Refusal struct {
	Reason string
}

func (r Refusal) Error() string { return r.Reason }

// authorize is the single decision point. Everything reaching the binary goes
// through it.
func (idx commandIndex) authorize(argv []string, mode Mode) (manifest.Command, error) {
	if len(argv) == 0 {
		return manifest.Command{}, Refusal{"no command given. Call runos_catalog to see what exists."}
	}
	root := argv[0]
	if strings.HasPrefix(root, "-") {
		return manifest.Command{}, Refusal{"the first argument must be a command, not a flag."}
	}

	c, attempted, ok := idx.resolveCommand(argv)
	if ok {
		if allowed, why := permits(c, mode); !allowed {
			return manifest.Command{}, Refusal{why}
		}
		return c, nil
	}

	// Not manifest-backed. The static allow-list is the only other way in.
	if want, listed := staticAllow[root]; listed && len(argv) == 1 {
		if want == ModeRO || mode == ModeRW {
			return manifest.Command{Command: root, Method: "GET"}, nil
		}
	}
	if why, known := staticDenyReason[root]; known {
		return manifest.Command{}, Refusal{fmt.Sprintf("`runos %s` is not available through this gateway: %s", root, why)}
	}
	return manifest.Command{}, Refusal{fmt.Sprintf(
		"no RunOS command matches `%s`. Call runos_catalog to see what exists. Commands are only reachable if the manifest declares them, so a command that works in a terminal may still be refused here.",
		attempted)}
}

// ---------- discovery ----------

// catalogGroups returns the top-level groups, the cheapest possible orientation.
func (idx commandIndex) catalogGroups(mode Mode) string {
	counts := map[string]int{}
	for path, c := range idx {
		if ok, _ := permits(c, mode); !ok {
			continue
		}
		counts[strings.SplitN(path, "/", 2)[0]]++
	}
	names := make([]string, 0, len(counts))
	for g := range counts {
		names = append(names, g)
	}
	sort.Strings(names)
	var b strings.Builder
	fmt.Fprintf(&b, "%d command groups available in %s mode. Call runos_catalog with a group to list its commands.\n\n", len(names), mode)
	for _, g := range names {
		fmt.Fprintf(&b, "  %-22s %d\n", g, counts[g])
	}
	return b.String()
}

// bigGroup is the point past which a flat listing stops being an orientation
// and becomes a dump. `services` alone holds 175 commands, 4,462 tokens, more
// than three times the whole cold-start path. Past this threshold the catalog
// tiers again on the next path segment.
const bigGroup = 40

// catalogSubgroups splits an oversized group by its next path segment, so
// `services` answers with valkey, postgresql, minio and the rest rather than
// 175 lines.
func (idx commandIndex) catalogSubgroups(group string, mode Mode, total int) string {
	counts := map[string]int{}
	direct := 0
	for path, c := range idx {
		if !strings.HasPrefix(path, group+"/") {
			continue
		}
		if ok, _ := permits(c, mode); !ok {
			continue
		}
		rest := strings.TrimPrefix(path, group+"/")
		if i := strings.Index(rest, "/"); i > 0 {
			counts[rest[:i]]++
		} else {
			direct++
		}
	}
	names := make([]string, 0, len(counts))
	for k := range counts {
		names = append(names, k)
	}
	sort.Strings(names)
	var b strings.Builder
	fmt.Fprintf(&b, "`%s` holds %d commands, too many to list at once. It splits into %d:\n\n", group, total, len(names))
	for _, n := range names {
		fmt.Fprintf(&b, "  %-26s %d\n", group+"/"+n, counts[n])
	}
	if direct > 0 {
		fmt.Fprintf(&b, "\n%d commands sit directly under `%s`.\n", direct, group)
	}
	if len(names) > 0 {
		fmt.Fprintf(&b, "\nCall runos_catalog with a sub-group, e.g. \"%s/%s\".\n", group, names[0])
	}
	return b.String()
}

// catalogGroup lists one group's commands, one line each.
// clusterScoped reports whether a command is served on a cluster-scoped route.
//
// The manifest expresses cluster scope in the ENDPOINT (`/:aid/:cid/nodes`), not
// as an input field, so `nodes list` has no fields at all and the catalog used to
// describe it as "Takes no arguments". The CLI then refuses the call with
// "cluster ID required". The catalog was stating something false about roughly
// every cluster-scoped read, which is the one thing a discovery layer must never
// do: the agent cannot check it against anything.
func clusterScoped(c manifest.Command) bool {
	return strings.Contains(c.Endpoint, "/:cid/") || strings.HasSuffix(c.Endpoint, "/:cid")
}

// signature renders a command's REQUIRED arguments inline, so a group listing is
// enough to call most commands directly.
//
// Measured 2026-09-02 on a five-command task: the gateway spent 11 catalog calls
// to run 5 commands, because the listing named commands but not their arguments,
// so the agent drilled group -> command -> exec for every one. Discovery scaled
// with the number of distinct commands, which is the opposite of what a cheap
// discovery layer should do. Required args are the part the agent cannot guess;
// optional ones stay behind the detail call so a 40-command group stays readable.
func signature(c manifest.Command) string {
	var req []string
	optional := 0
	if clusterScoped(c) {
		req = append(req, "--cid")
	}
	if c.Input == nil || len(c.Input.Fields) == 0 {
		if len(req) == 0 {
			return "(no args)"
		}
		return strings.Join(req, " ")
	}
	for _, f := range c.Input.Fields {
		if !f.Required {
			optional++
			continue
		}
		if f.Positional {
			req = append(req, "<"+f.Name+">")
		} else {
			req = append(req, "--"+f.Name)
		}
	}
	var parts []string
	if len(req) > 0 {
		parts = append(parts, strings.Join(req, " "))
	} else {
		parts = append(parts, "(no required args)")
	}
	if optional > 0 {
		parts = append(parts, fmt.Sprintf("[+%d optional]", optional))
	}
	return strings.Join(parts, " ")
}

func (idx commandIndex) catalogGroup(group string, mode Mode) string {
	type row struct{ path, sig, desc string }
	var rows []row
	for path, c := range idx {
		if !strings.HasPrefix(path, group+"/") && path != group {
			continue
		}
		if ok, _ := permits(c, mode); !ok {
			continue
		}
		rows = append(rows, row{path, signature(c), firstSentence(c.Description)})
	}
	if len(rows) == 0 {
		return fmt.Sprintf("No commands in group `%s` are available in %s mode. Call runos_catalog with no argument for the groups that are.", group, mode)
	}
	if len(rows) > bigGroup && !strings.Contains(group, "/") {
		return idx.catalogSubgroups(group, mode, len(rows))
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].path < rows[j].path })
	var b strings.Builder
	fmt.Fprintf(&b, "%d commands in `%s`. REQUIRED arguments are shown, so you can call most of these directly with the runos tool. Fetch a full command path only when you need its optional arguments.\n\n", len(rows), group)
	for _, r := range rows {
		fmt.Fprintf(&b, "  %-38s %-34s %s\n", strings.ReplaceAll(r.path, "/", " "), r.sig, r.desc)
	}
	return b.String()
}

// catalogCommand is the full detail for one command, fetched only when the
// agent is about to run it.
func (idx commandIndex) catalogCommand(path string, mode Mode) string {
	c, ok := idx[path]
	if !ok {
		return fmt.Sprintf("No command `%s`. Call runos_catalog with its group to see what exists.", path)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "runos %s\n\n%s\n\n", strings.ReplaceAll(c.Command, "/", " "), c.Description)
	if allowed, why := permits(c, mode); !allowed {
		fmt.Fprintf(&b, "REFUSED IN THIS MODE: %s\n\n", why)
	}
	if dynacmd.IsDestructiveCommand(c) {
		fmt.Fprintf(&b, "DESTRUCTIVE. Requires confirm=true, and the user must have agreed to this exact target.\n\n")
	}
	if c.ReturnsJob {
		fmt.Fprintf(&b, "Returns a job. Poll it rather than assuming completion.\n\n")
	}
	if clusterScoped(c) {
		b.WriteString("Arguments:\n  --cid                      string  (required)  Target cluster. A configured default cid satisfies it; without either the call is refused.\n")
		if c.Input == nil || len(c.Input.Fields) == 0 {
			return b.String()
		}
	} else if c.Input == nil || len(c.Input.Fields) == 0 {
		b.WriteString("Takes no arguments.\n")
		return b.String()
	} else {
		b.WriteString("Arguments:\n")
	}
	for _, f := range c.Input.Fields {
		req := ""
		if f.Required {
			req = "  (required)"
		}
		name := "--" + f.Name
		if f.Positional {
			name = "<" + f.Name + ">"
		}
		fmt.Fprintf(&b, "  %-26s %-8s%s %s\n", name, f.Type, req, firstSentence(f.Description))
		if len(f.Enum) > 0 {
			fmt.Fprintf(&b, "  %-26s one of: %s\n", "", strings.Join(f.Enum, ", "))
		}
	}
	return b.String()
}

func firstSentence(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if i := strings.Index(s, ". "); i > 0 && i < 160 {
		return s[:i+1]
	}
	if len(s) > 160 {
		return s[:157] + "..."
	}
	return s
}

// ---------- execution ----------

// runGateway spawns THIS binary with the given argv.
//
// argv arrives as a JSON array and is passed to exec directly. It NEVER goes
// through a shell, so `;`, `|` and `$(...)` are ordinary characters in an
// argument rather than syntax.
func runGateway(ctx context.Context, argv []string) (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("cannot locate the runos binary: %w", err)
	}
	args := append([]string{}, argv...)
	if !hasFlag(args, "--json") {
		args = append(args, "--json")
	}
	cmd := exec.CommandContext(ctx, self, args...)
	// No TTY: the CLI must not try to prompt. Anything needing a human
	// answer has to be refused before it reaches here.
	cmd.Stdin = nil
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func hasFlag(argv []string, flag string) bool {
	for _, a := range argv {
		if a == flag || strings.HasPrefix(a, flag+"=") {
			return true
		}
	}
	return false
}

const gatewayTimeout = 10 * time.Minute

// GatewayTools are the tools the gateway exposes, in place of one per command.
func GatewayTools(mode Mode) []Tool {
	return []Tool{
		{
			Name: "runos_catalog",
			// The wording is load-bearing. It previously said "Read a command's entry
			// before running it", and the agent obeyed literally: measured at 11 to 12
			// catalog calls to run 5 commands, drilling group -> command -> exec every
			// time, even after the group listing began carrying the required arguments.
			// The prose beat the structure, so the prose had to change too.
			Description: "Discover RunOS commands. mcp_bootstrap already gave you the group index, " +
				"so start at `group` rather than calling this with no argument. PASS EVERY GROUP YOU " +
				"NEED AT ONCE: `group` takes a list, e.g. [\"nodes\",\"apps\",\"vms\"], and one call " +
				"beats five. A group listing shows each command's REQUIRED arguments, so it is enough " +
				"to call most commands: go straight to the runos tool from there. Use `command` " +
				"(also a list) only when you need OPTIONAL arguments, enum values, or a full description.",
			InputSchema: InputSchema{
				Type: "object",
				Properties: map[string]Property{
					"group":   {Type: "array", Description: "Group names, e.g. [\"vms\",\"nodes\"]. A single string is accepted too.", Items: &Property{Type: "string"}},
					"command": {Type: "array", Description: "Full command paths, e.g. [\"vms/snapshot\"]. A single string is accepted too.", Items: &Property{Type: "string"}},
				},
			},
			Annotations: &ToolAnnotations{ReadOnlyHint: true},
		},
		{
			Name: "runos",
			Description: "Run ONE RunOS command. `args` is the command line as an ARRAY, " +
				"e.g. [\"vms\",\"snapshot\",\"--vmid\",\"abc12\"]. Never a single string. " +
				"One command per call, so you see each result before deciding the next step. " +
				"Output is JSON. If you do not know the command, call runos_catalog with " +
				"its `group` once: that listing names the required arguments and is normally all you need.",
			InputSchema: InputSchema{
				Type: "object",
				Properties: map[string]Property{
					"args": {
						Type:        "array",
						Description: "Command and arguments, one array element each. Do not include the leading \"runos\".",
						Items:       &Property{Type: "string"},
					},
					confirmArgName: {
						Type:        "boolean",
						Description: "Required for a destructive command. Set true only after the user has agreed to this exact target.",
					},
				},
				Required: []string{"args"},
			},
			Annotations: gatewayAnnotations(mode),
		},
	}
}

func gatewayAnnotations(mode Mode) *ToolAnnotations {
	if mode == ModeRO {
		return &ToolAnnotations{ReadOnlyHint: true}
	}
	return &ToolAnnotations{}
}

// argvFrom pulls the argv array out of the tool arguments.
//
// A STRING IS REFUSED ON PURPOSE. Accepting one would mean parsing shell
// grammar here, and every such parser eventually disagrees with a real shell
// about quoting. The array has no grammar to disagree about.
func argvFrom(args map[string]any) ([]string, error) {
	raw, ok := args["args"]
	if !ok {
		return nil, Refusal{"`args` is required, as an array of strings."}
	}
	if s, isString := raw.(string); isString {
		return nil, Refusal{fmt.Sprintf(
			"`args` must be an ARRAY of strings, not one string. You passed %q. Send [\"vms\",\"list\"] rather than \"vms list\".", s)}
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, Refusal{"`args` must be an array of strings."}
	}
	argv := make([]string, 0, len(list))
	for i, v := range list {
		s, ok := v.(string)
		if !ok {
			return nil, Refusal{fmt.Sprintf("every element of `args` must be a string; element %d is not.", i)}
		}
		argv = append(argv, s)
	}
	return argv, nil
}

// MarshalArgs is a helper for tests and logging.
func MarshalArgs(argv []string) string { b, _ := json.Marshal(argv); return string(b) }

// ---------- server wiring ----------

func (s *Server) textResult(req *Request, text string) *Response {
	return &Response{JSONRPC: "2.0", ID: req.ID,
		Result: CallToolResult{Content: []ContentBlock{{Type: "text", Text: text}}}}
}

func (s *Server) errableResult(req *Request, text string, isErr bool) *Response {
	return &Response{JSONRPC: "2.0", ID: req.ID,
		Result: CallToolResult{Content: []ContentBlock{{Type: "text", Text: text}}, IsError: isErr}}
}

// maxBatch bounds a single batched call. A batch exists to remove round trips,
// not to let one call return unbounded output or fan out unbounded work.
const maxBatch = 20

// stringOrList reads a field that accepts either one string or a list of them,
// so `group` and `groups` are the same parameter to a caller.
func stringOrList(args map[string]any, keys ...string) []string {
	var out []string
	for _, k := range keys {
		switch v := args[k].(type) {
		case string:
			if s := strings.TrimSpace(v); s != "" {
				out = append(out, s)
			}
		case []any:
			for _, e := range v {
				if s, ok := e.(string); ok {
					if s = strings.TrimSpace(s); s != "" {
						out = append(out, s)
					}
				}
			}
		}
	}
	return out
}

func (s *Server) handleCatalog(args map[string]any) string {
	// Commands and groups both take a list. An agent inventorying a cluster asked
	// for five groups in five calls; every one of those round trips re-sent the
	// whole conversation to learn something the manifest already knew.
	if cmds := stringOrList(args, "command", "commands"); len(cmds) > 0 {
		return s.catalogMany(cmds, func(x string) string {
			return s.gatewayIdx.catalogCommand(strings.TrimSpace(x), s.gatewayMode)
		})
	}
	if groups := stringOrList(args, "group", "groups"); len(groups) > 0 {
		return s.catalogMany(groups, func(x string) string {
			return s.gatewayIdx.catalogGroup(strings.Trim(x, "/"), s.gatewayMode)
		})
	}
	return s.gatewayIdx.catalogGroups(s.gatewayMode)
}

func (s *Server) catalogMany(items []string, render func(string) string) string {
	if len(items) == 1 {
		return render(items[0])
	}
	if len(items) > maxBatch {
		return fmt.Sprintf("Too many at once (%d). Ask for at most %d per call.", len(items), maxBatch)
	}
	var b strings.Builder
	for i, it := range items {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "===== %s =====\n%s", it, render(it))
	}
	return b.String()
}

// handleGatewayExec authorizes then runs. Authorization happens BEFORE the
// process is spawned, never after.
// handleGatewayExec runs exactly ONE command.
//
// Execution is deliberately NOT batchable, though discovery is. Every executed
// command is a decision: the caller reads its result before choosing what to do
// next, and a batch removes that decision point and buries a mid-sequence
// failure inside one aggregate result. Discovery has no such property, which is
// why runos_catalog takes lists and this does not.
func (s *Server) handleGatewayExec(args map[string]any) (string, bool) {
	argv, err := argvFrom(args)
	if err != nil {
		return err.Error(), true
	}
	cmd, err := s.gatewayIdx.authorize(argv, s.gatewayMode)
	if err != nil {
		return err.Error(), true
	}
	if dynacmd.IsDestructiveCommand(cmd) {
		if confirmed, _ := args[confirmArgName].(bool); !confirmed {
			return fmt.Sprintf(
				"`runos %s` is destructive and cannot be undone. Re-send with confirm=true, and only after the user has agreed to this exact target.",
				strings.Join(argv, " ")), true
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), gatewayTimeout)
	defer cancel()
	out, runErr := runGateway(ctx, argv)
	if runErr != nil {
		return fmt.Sprintf("`runos %s` failed: %v\n\n%s", strings.Join(argv, " "), runErr, out), true
	}
	if strings.TrimSpace(out) == "" {
		return fmt.Sprintf("`runos %s` succeeded and returned no output.", strings.Join(argv, " ")), false
	}
	return out, false
}
