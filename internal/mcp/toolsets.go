package mcp

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/runos-official/cli/internal/manifest"
)

// Toolsets narrows the exposed tool surface to the managed-service types an
// account actually runs.
//
// WHY. 152 of the read server's 315 tools are service-type specific
// (services/<type>/...), about 17,100 tokens. An account running seven of the
// twenty-two types pays for fifteen it has never deployed. Measured on a real
// account: 46,195 -> 33,745 tokens, 27% off, and an account running two or
// three types saves far more. Unlike the retired gateway this costs NOTHING at
// run time: the tools that remain keep their full schemas in the cached
// prefix, and nothing has to be discovered mid-conversation.
//
// FAIL OPEN, ALWAYS. Every unreadable, missing, empty or malformed state means
// "expose everything". A cache that cannot be read must never leave an operator
// unable to see their own platform, and a first run has no cache at all.
type Toolsets struct {
	mu sync.RWMutex
	// scoped is false until a usable cache is loaded. False = expose everything.
	scoped bool
	// inUse are the service types the account was last seen running.
	inUse map[string]struct{}
	// extra are types added by hand, either in the cache file or at run time
	// through runos_tools_enable. Kept separate from inUse so a refresh that
	// re-reads the account cannot silently drop a deliberate addition.
	extra map[string]struct{}
	// known is every service type the manifest defines, so the enable tool can
	// name what is available but hidden, and reject a type that does not exist.
	known map[string]struct{}
	// platformManaged are the types conductor says IT owns rather than the user:
	// the TLS issuer, the ingress, the VPN. Hidden even when the account runs
	// them, because nobody asks an agent to manage the ingress in the ordinary
	// course of work. Every one is a runos_tools_enable call away, which matters
	// because cert-manager is what a stuck certificate needs and wireguard is
	// what a node delete has to move.
	platformManaged map[string]struct{}
	// virtInstalled is false when no cluster on the account runs KubeVirt, and
	// then the whole VM surface is hidden. That surface is 31 tools and about
	// 17,000 tokens, roughly HALF the read server, because its tools carry the
	// longest descriptions in the manifest. An account with no virtualisation
	// was carrying all of it.
	virtInstalled bool
}

// vmGroups are the top-level command groups that only mean anything once
// KubeVirt is installed. Listed explicitly rather than matched on a "vm" prefix
// so that a future group starting with those letters cannot be swept in by
// accident, and so this list is somewhere a reader can find it.
var vmGroups = map[string]struct{}{
	"vms": {}, "virt": {}, "vm-groups": {}, "vm-images": {}, "vm-networks": {},
	"vm-addresses": {}, "vm-address-blocks": {}, "vm-events": {}, "vm-usage": {},
}

// Platform-managed types come from conductor, which marks them
// `isPlatformManaged` in the service-type registry. Deliberately NOT a list
// here: a copy in the CLI drifts the moment a type is added or reclassified,
// and this side would then hide something conductor no longer considers
// platform-owned, or miss one it does.

// virtCapability is the name runos_tools_enable takes to load the whole VM
// surface. Not a service type, so it is kept apart from the type names and
// accepted alongside them.
const virtCapability = "virt"

// serviceTypeOf returns the managed-service type a manifest command belongs to.
//
// Derived from the manifest path (`services/<type>/...`), never guessed from
// the tool name: `services_list` and `services_dependencies` are generic and
// have no type, and a name-shape regex would have to encode that by accident.
func serviceTypeOf(commandPath string) string {
	parts := strings.Split(commandPath, "/")
	if len(parts) < 3 || parts[0] != "services" {
		return ""
	}
	if strings.HasPrefix(parts[1], "{") {
		return ""
	}
	return parts[1]
}

// newUnscoped builds the known-type index with no filtering applied.
func newUnscoped(m *manifest.Manifest) *Toolsets {
	ts := &Toolsets{
		inUse:           map[string]struct{}{},
		extra:           map[string]struct{}{},
		known:           map[string]struct{}{},
		platformManaged: map[string]struct{}{},
		// Unscoped hides nothing, VM tools included.
		virtInstalled: true,
	}
	if m != nil {
		for _, c := range m.Commands {
			if t := serviceTypeOf(c.Command); t != "" {
				ts.known[t] = struct{}{}
			}
		}
	}
	return ts
}

// NewToolsets is the unscoped constructor, for callers with no account context
// (tests, and anything that has not fetched).
func NewToolsets(m *manifest.Manifest) *Toolsets { return newUnscoped(m) }

// FetchToolsets asks conductor which managed-service types this account runs.
//
// Conductor answers because it already holds the data: deriving it here would
// mean clusters/list plus services/list per cluster, the same fan-out with N
// extra round trips, and a second on-disk cache that goes stale the moment
// someone installs a service. Conductor caches for a day, invalidates on any
// service create or delete, and serves the previous answer while it recomputes.
//
// EVERY FAILURE RETURNS UNSCOPED. No token, no network, a slow conductor, an
// old conductor with no such route, a malformed body: all of them expose every
// tool. A scoping mechanism must never be the reason an operator cannot see
// their own platform, and this runs at MCP startup where nobody is watching.
func FetchToolsets(m *manifest.Manifest, baseURL, accountID, token string, timeout time.Duration) *Toolsets {
	ts := newUnscoped(m)
	if baseURL == "" || accountID == "" || token == "" {
		return ts
	}
	url := fmt.Sprintf("%s/%s/cli/toolsets", strings.TrimRight(baseURL, "/"), accountID)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return ts
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return ts
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ts
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return ts
	}
	var out struct {
		Scoped               bool     `json:"scoped"`
		ServiceTypes         []string `json:"serviceTypes"`
		VirtInstalled        bool     `json:"virtInstalled"`
		PlatformManagedTypes []string `json:"platformManagedTypes"`
	}
	if json.Unmarshal(body, &out) != nil || !out.Scoped {
		return ts
	}
	// An account with clusters but genuinely no services scopes to nothing but
	// the generic service tools, which is correct. An account with no answer at
	// all took the !out.Scoped path above.
	for _, x := range out.ServiceTypes {
		ts.inUse[x] = struct{}{}
	}
	for _, x := range out.PlatformManagedTypes {
		ts.platformManaged[x] = struct{}{}
	}
	ts.virtInstalled = out.VirtInstalled
	ts.scoped = true
	return ts
}

// permits reports whether a command may be exposed.
func (ts *Toolsets) permits(commandPath string) bool {
	if ts == nil {
		return true
	}
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	if !ts.scoped {
		return true
	}

	// The VM surface goes as a whole: these groups are meaningless without
	// KubeVirt, and half of them would only ever return "not installed".
	group := commandPath
	if i := strings.Index(group, "/"); i >= 0 {
		group = group[:i]
	}
	if _, isVM := vmGroups[group]; isVM {
		if ts.virtInstalled {
			return true
		}
		_, on := ts.extra[virtCapability]
		return on
	}

	t := serviceTypeOf(commandPath)
	if t == "" {
		return true // not service-type specific: always exposed
	}
	if _, ok := ts.extra[t]; ok {
		return true
	}
	// Platform-owned services are hidden even when present.
	if _, platform := ts.platformManaged[t]; platform {
		return false
	}
	_, ok := ts.inUse[t]
	return ok
}

// Hidden lists the known types currently filtered out, for the enable tool.
func (ts *Toolsets) Hidden() []string {
	if ts == nil {
		return nil
	}
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	if !ts.scoped {
		return nil
	}
	var out []string
	for t := range ts.known {
		if _, ok := ts.extra[t]; ok {
			continue
		}
		_, platform := ts.platformManaged[t]
		_, used := ts.inUse[t]
		// A platform-owned type is hidden even when in use; any other type is
		// hidden only when the account does not run it.
		if platform || !used {
			out = append(out, t)
		}
	}
	if !ts.virtInstalled {
		if _, on := ts.extra[virtCapability]; !on {
			out = append(out, virtCapability)
		}
	}
	sort.Strings(out)
	return out
}

// Enable adds types for the life of this process. Returns the ones newly
// added and the ones that name no service type in the manifest.
func (ts *Toolsets) Enable(types []string) (added, unknown []string) {
	if ts == nil {
		return nil, types
	}
	ts.mu.Lock()
	defer ts.mu.Unlock()
	for _, raw := range types {
		t := strings.TrimSpace(strings.ToLower(raw))
		t = strings.TrimPrefix(t, "services/")
		t = strings.TrimPrefix(t, "services_")
		if t == "" {
			continue
		}
		if t == virtCapability {
			if _, on := ts.extra[virtCapability]; on || ts.virtInstalled {
				continue
			}
			ts.extra[virtCapability] = struct{}{}
			added = append(added, virtCapability)
			continue
		}
		if _, ok := ts.known[t]; !ok {
			unknown = append(unknown, raw)
			continue
		}
		// A platform-owned type is hidden despite being in use, so inUse must
		// not short-circuit it here or it could never be enabled.
		if _, platform := ts.platformManaged[t]; !platform {
			if _, ok := ts.inUse[t]; ok {
				continue
			}
		}
		if _, ok := ts.extra[t]; ok {
			continue
		}
		ts.extra[t] = struct{}{}
		added = append(added, t)
	}
	sort.Strings(added)
	return added, unknown
}

// Scoped reports whether filtering is active at all.
func (ts *Toolsets) Scoped() bool {
	if ts == nil {
		return false
	}
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	return ts.scoped
}

// enableToolDescription names the hidden types inline, so the caller can widen
// the surface without a discovery round trip. Costs about thirty tokens once,
// against the twelve thousand the filtering saves.
func (ts *Toolsets) enableToolDescription() string {
	if ts == nil {
		return ""
	}
	hidden := ts.Hidden()
	if len(hidden) == 0 {
		return "Add managed-service tools to this session. Every service type this account runs is already available, so this is only needed for a type you are about to install."
	}
	return fmt.Sprintf(
		"Load more RunOS tools into this session. The surface is scoped to what this account actually runs, so these are NOT loaded: %s. Pass one or more to load their tools immediately; this changes nothing on the account and installs nothing. `virt` loads the whole VM surface (vms, virt, vm-groups, vm-images, vm-networks). cert-manager, traefik and wireguard are running but hidden because the platform manages them; load one if you are debugging certificates, ingress or the VPN.",
		strings.Join(hidden, ", "))
}

// toolsEnableToolName is the one always-listed tool that widens the surface.
const toolsEnableToolName = "runos_tools_enable"

// handleToolsEnable loads more managed-service tools for this session.
//
// Session-scoped on purpose: it does not write the cache, install anything or
// change the account. The caller asked to SEE a type's tools, which is a
// read-shaped act, so it carries readOnlyHint and needs no confirmation.
func (s *Server) handleToolsEnable(args map[string]any) (string, bool, error) {
	raw, ok := args["types"]
	if !ok {
		return "`types` is required: an array of service types, e.g. [\"kafka\"].", false, nil
	}
	var want []string
	switch v := raw.(type) {
	case string:
		// A single string is the obvious mistake; take it rather than refuse.
		want = []string{v}
	case []any:
		for _, e := range v {
			if str, ok := e.(string); ok {
				want = append(want, str)
			}
		}
	default:
		return "`types` must be an array of strings, e.g. [\"kafka\",\"vllm\"].", false, nil
	}
	if len(want) == 0 {
		return "`types` was empty. Pass at least one service type.", false, nil
	}
	added, unknown := s.toolsets.Enable(want)
	var b strings.Builder
	if len(added) > 0 {
		fmt.Fprintf(&b, "Loaded tools for: %s. They are available now.\n", strings.Join(added, ", "))
	}
	if len(unknown) > 0 {
		fmt.Fprintf(&b, "No such service type: %s.\n", strings.Join(unknown, ", "))
		if hidden := s.toolsets.Hidden(); len(hidden) > 0 {
			fmt.Fprintf(&b, "Available to load: %s.\n", strings.Join(hidden, ", "))
		}
	}
	if len(added) == 0 && len(unknown) == 0 {
		b.WriteString("Already loaded; nothing changed.\n")
	}
	return b.String(), len(added) > 0, nil
}

// SetToolsets installs the scoping decision. Nil is ignored, so a caller that
// could not fetch leaves the server unscoped rather than empty.
func (s *Server) SetToolsets(ts *Toolsets) {
	if ts == nil {
		return
	}
	s.toolsets = ts
}
