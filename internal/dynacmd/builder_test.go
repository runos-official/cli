package dynacmd

import (
	"testing"

	"github.com/runos-official/cli/internal/manifest"
)

// Regression test for I2-3c (TEST_LOG.md): manifest field names that
// arrive as camelCase (publicRead, accessKey, allowDirty, ...) must
// register as kebab-case CLI flags (--public-read, --access-key,
// --allow-dirty), matching the rest of the RunOS CLI flag spelling.
// The body sent to conductor still uses the original camelCase name.
func TestFlagNameFor(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		// Empty string is a no-op.
		{"", ""},
		// Single-word lowercase passes through.
		{"id", "id"},
		{"cid", "cid"},
		{"bucket", "bucket"},
		// snake_case passes through (already non-camel).
		{"checkpoint_completion_target", "checkpoint_completion_target"},
		// Two-word camelCase: I2-3c canonical case.
		{"publicRead", "public-read"},
		{"appId", "app-id"},
		{"allowDirty", "allow-dirty"},
		{"configPath", "config-path"},
		{"expectedCid", "expected-cid"},
		// Three-word camelCase.
		{"cpuRequestMc", "cpu-request-mc"},
		{"customSecretEnvVars", "custom-secret-env-vars"},
		// Acronym-aware kebab (I13-G): consecutive uppercase letters
		// collapse into one token. Trailing acronyms stay together;
		// acronym→title boundaries split.
		{"serviceID", "service-id"},
		{"publicURL", "public-url"},
		{"URLPath", "url-path"},
		{"JSONResponse", "json-response"},
		{"minIOOsid", "min-io-osid"},
		// All-uppercase reads as one acronym word.
		{"API", "api"},
		// Single trailing acronym after lower run.
		{"resourceCPU", "resource-cpu"},
	}
	for _, tt := range cases {
		t.Run(tt.in, func(t *testing.T) {
			got := flagNameFor(tt.in)
			if got != tt.want {
				t.Errorf("flagNameFor(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// Idempotency: applying flagNameFor twice should be a fixpoint for
// kebab-case inputs. Important because some callers pass already-kebab
// names (the manifest occasionally normalises this server-side).
func TestFlagNameFor_KebabIdempotent(t *testing.T) {
	for _, in := range []string{"public-read", "app-id", "config-path"} {
		if got := flagNameFor(in); got != in {
			t.Errorf("flagNameFor(%q) = %q, want %q (idempotent)", in, got, in)
		}
	}
}

// Regression target for I6-H: backtick-quoted words in flag descriptions
// were rendering as the flag's value-type column (pflag's UnquoteUsage
// treats the first backtick-quoted token as a type-name override). The
// fix strips backticks before passing to pflag so Markdown code spans in
// descriptions no longer collide with the value-type column. Empty
// strings, ASCII without backticks, multi-line descriptions, and adja-
// cent backticks all round-trip cleanly minus the backtick characters.
func TestSanitizeFlagDescription(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"no backticks", "Plain description with no markdown.", "Plain description with no markdown."},
		{"single backtick pair", "Pass via `-f body.yaml` to add objects.", "Pass via -f body.yaml to add objects."},
		{"two backtick pairs", "the CLI `--add` string-list flag is not usable for this field, pass via `-f body.yaml`", "the CLI --add string-list flag is not usable for this field, pass via -f body.yaml"},
		{"backtick at start", "`custom` flips RRC", "custom flips RRC"},
		{"unbalanced backtick", "trailing ` only", "trailing  only"},
		{"adjacent backticks", "``empty``", "empty"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeFlagDescription(tt.in); got != tt.want {
				t.Errorf("sanitizeFlagDescription(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestCountPositionalFields and TestHasNonPositionalInput pin the
// helpers driving I11-X (cap positional args at the manifest count to
// refuse typos) and I11-U (don't register `-f` for commands whose only
// input is a path-param positional like `apps replace-manifest <id>`).
func TestCountPositionalFields(t *testing.T) {
	cases := []struct {
		name string
		in   manifest.Command
		want int
	}{
		{"nil input", manifest.Command{}, 0},
		{"empty fields", manifest.Command{Input: &manifest.Input{}}, 0},
		{
			"two positional + one flag-only",
			manifest.Command{Input: &manifest.Input{Fields: []manifest.Field{
				{Name: "cid", Positional: true},
				{Name: "id", Positional: true},
				{Name: "name"},
			}}},
			2,
		},
		{
			"only non-positional fields",
			manifest.Command{Input: &manifest.Input{Fields: []manifest.Field{
				{Name: "name"},
				{Name: "expiresAt"},
			}}},
			0,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := countPositionalFields(tt.in); got != tt.want {
				t.Errorf("countPositionalFields(%+v) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestHasNonPositionalInput(t *testing.T) {
	cases := []struct {
		name string
		in   manifest.Command
		want bool
	}{
		{"nil input", manifest.Command{}, false},
		{
			"only positional path params (apps replace-manifest shape)",
			manifest.Command{Input: &manifest.Input{Fields: []manifest.Field{
				{Name: "id", Positional: true, Required: true},
			}}},
			false,
		},
		{
			"positional + body field",
			manifest.Command{Input: &manifest.Input{Fields: []manifest.Field{
				{Name: "id", Positional: true},
				{Name: "name"},
			}}},
			true,
		},
		{
			"positional + boolean flag",
			manifest.Command{Input: &manifest.Input{
				Fields: []manifest.Field{{Name: "id", Positional: true}},
				Flags:  []manifest.Flag{{Name: "force", Default: false}},
			}},
			true,
		},
		{
			"all non-positional",
			manifest.Command{Input: &manifest.Input{Fields: []manifest.Field{
				{Name: "name"},
				{Name: "expiresAt"},
			}}},
			true,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasNonPositionalInput(tt.in); got != tt.want {
				t.Errorf("hasNonPositionalInput(%+v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// TestPrimaryPositionalIDField pins the helper driving I12-L: register
// `--id` as an alias for the single `*Id` positional so users don't
// have to remember the canonical kebab name (`--job-id`, `--override-id`)
// when the natural form is `--id`. Returns empty for ambiguous cases
// (multiple `*Id` positionals) so the alias doesn't fire there.
func TestPrimaryPositionalIDField(t *testing.T) {
	cases := []struct {
		name string
		in   manifest.Command
		want string
	}{
		{"no input", manifest.Command{}, ""},
		{
			"no positional fields",
			manifest.Command{Input: &manifest.Input{Fields: []manifest.Field{
				{Name: "name"},
			}}},
			"",
		},
		{
			"single jobId (jobs/show)",
			manifest.Command{Input: &manifest.Input{Fields: []manifest.Field{
				{Name: "jobId", Positional: true, Required: true},
			}}},
			"job-id",
		},
		{
			"plain id + overrideId returns empty (apps/overrides/show; --id stays the app)",
			manifest.Command{Input: &manifest.Input{Fields: []manifest.Field{
				{Name: "id", Positional: true, Required: true},
				{Name: "overrideId", Positional: true, Required: true},
			}}},
			"",
		},
		{
			"plain id + cliUploadId returns empty (apps/prepare-cli-pull)",
			manifest.Command{Input: &manifest.Input{Fields: []manifest.Field{
				{Name: "id", Positional: true, Required: true},
				{Name: "cliUploadId", Positional: true, Required: true},
			}}},
			"",
		},
		{
			"two *Id positionals returns empty (ambiguous)",
			manifest.Command{Input: &manifest.Input{Fields: []manifest.Field{
				{Name: "integrationId", Positional: true},
				{Name: "zoneId", Positional: true},
			}}},
			"",
		},
		{
			"plain `id` positional returns empty (already matches --id)",
			manifest.Command{Input: &manifest.Input{Fields: []manifest.Field{
				{Name: "id", Positional: true},
			}}},
			"",
		},
		{
			"cid is excluded from the *Id scan",
			manifest.Command{Input: &manifest.Input{Fields: []manifest.Field{
				{Name: "cid", Positional: true},
				{Name: "overrideId", Positional: true},
			}}},
			"override-id",
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := primaryPositionalIDField(tt.in); got != tt.want {
				t.Errorf("primaryPositionalIDField(%+v) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestMultiPositionalIDFlagNames pins the I13-D helper that decides
// when to suggest specific kebab-flag names on `unknown flag: --id`
// errors. Returns nil for single- or zero-`*Id` commands (since the
// I12-L alias already handles those) and for commands carrying a plain
// `id` field alongside (see I12-L plain-id rule).
func TestMultiPositionalIDFlagNames(t *testing.T) {
	cases := []struct {
		name string
		in   manifest.Command
		want []string
	}{
		{"no input", manifest.Command{}, nil},
		{
			"single jobId (I12-L alias fires)",
			manifest.Command{Input: &manifest.Input{Fields: []manifest.Field{
				{Name: "jobId", Positional: true},
			}}},
			nil,
		},
		{
			"jobs/workitem-logs (jobId + workItemId)",
			manifest.Command{Input: &manifest.Input{Fields: []manifest.Field{
				{Name: "jobId", Positional: true},
				{Name: "workItemId", Positional: true},
			}}},
			[]string{"--job-id", "--work-item-id"},
		},
		{
			"integrations/dns/records (integrationId + zoneId)",
			manifest.Command{Input: &manifest.Input{Fields: []manifest.Field{
				{Name: "integrationId", Positional: true},
				{Name: "zoneId", Positional: true},
			}}},
			[]string{"--integration-id", "--zone-id"},
		},
		{
			"plain id + overrideId (apps/overrides/show) returns just override-id since plain id is skipped",
			manifest.Command{Input: &manifest.Input{Fields: []manifest.Field{
				{Name: "id", Positional: true},
				{Name: "overrideId", Positional: true},
			}}},
			nil,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := multiPositionalIDFlagNames(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("got[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// TestConveniencePositionalFields pins the I13-H allow-list that lets
// `tools/domain-check example.com` work positionally even though the
// manifest field isn't marked positional. Keep the list short and
// explicit so it doesn't drift into unexpected commands.
func TestConveniencePositionalFields(t *testing.T) {
	cases := []struct {
		path string
		want []string
	}{
		{"tools/domain-check", []string{"domain"}},
		{"apps/logs", nil},
		{"unknown/command", nil},
	}
	for _, tt := range cases {
		t.Run(tt.path, func(t *testing.T) {
			got := conveniencePositionalFields(manifest.Command{Command: tt.path})
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("got[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// TestConveniencePositionalSatisfies pins the leaf-RunE missing-required
// gate's view of convenience-positional args. The fieldName has to
// appear in conveniencePos AND the args slice has to carry a non-empty
// value at the corresponding offset.
func TestConveniencePositionalSatisfies(t *testing.T) {
	conv := []string{"domain"}
	cases := []struct {
		name      string
		fieldName string
		args      []string
		start     int
		want      bool
	}{
		{"satisfied with single arg", "domain", []string{"example.com"}, 0, true},
		{"empty arg not satisfied", "domain", []string{""}, 0, false},
		{"no args not satisfied", "domain", []string{}, 0, false},
		{"field not in conv list", "expectedCid", []string{"example.com"}, 0, false},
		{"start offset beyond args", "domain", []string{"example.com"}, 1, false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := conveniencePositionalSatisfies(tt.fieldName, conv, tt.args, tt.start)
			if got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// TestFlagTypeOverride pins the I14-F (command, field) → flag-type
// override map. Pod-logs `since` is registered as a string so
// `--since 5m` and `--since 300` both parse via cobra; the executor
// converts to int seconds at body-build time. Every other pair returns
// "" so generic flag registration runs unchanged. I17-F widened the
// `apps/logs`-only allow-list to every services/<type>/{id}/logs.
func TestFlagTypeOverride(t *testing.T) {
	cases := []struct {
		cmdPath   string
		fieldName string
		want      string
	}{
		{"apps/logs", "since", "string"},
		{"apps/logs", "tail", ""},
		{"apps/logs", "id", ""},
		{"apps/list", "since", ""},
		// I17-F: service-type logs paths get the same override.
		{"services/postgresql/{id}/logs", "since", "string"},
		{"services/valkey/{id}/logs", "since", "string"},
		{"services/clickhouse/{id}/logs", "since", "string"},
		{"services/netbird-server/{id}/logs", "since", "string"},
		{"services/postgresql/{id}/logs", "tail", ""},
		// Non-logs service paths must NOT match — they have different
		// shapes and shouldn't get the duration-string parsing.
		{"services/postgresql/{id}/show", "since", ""},
		// builds/logs has a different schema (limit, not since); must
		// not match.
		{"builds/logs", "since", ""},
		{"", "", ""},
	}
	for _, tt := range cases {
		t.Run(tt.cmdPath+"/"+tt.fieldName, func(t *testing.T) {
			if got := flagTypeOverride(tt.cmdPath, tt.fieldName); got != tt.want {
				t.Errorf("flagTypeOverride(%q, %q) = %q, want %q", tt.cmdPath, tt.fieldName, got, tt.want)
			}
		})
	}
}

// TestExtraFieldsFor pins the I24-U per-command extra-fields allow-list.
// Entries are body fields the conductor accepts but doesn't advertise
// in the manifest yet; the CLI registers a kebab flag for each so the
// feature is reachable without dropping to `-f body.yaml`. Each entry
// should be temporary — once the field lands in the manifest the
// generic field-flag path takes over and the entry can be removed.
func TestExtraFieldsFor(t *testing.T) {
	// Conductor 15.0.0 rolled back the auto-provision CI flow that
	// I24-U's `apps_add --provision-ci-variables` opted into; the entry
	// was removed. The hook is intentionally empty at rest. The
	// invariant covered here: extraFieldsFor returns nil for every
	// command path so the manifest stays the single source of truth
	// for the body-field surface.
	for _, cmd := range []string{"apps/add", "apps/logs", "apps/update", "services/postgresql/{id}/logs", "clusters/list", ""} {
		if got := extraFieldsFor(cmd); got != nil {
			t.Errorf("extraFieldsFor(%q) = %+v, want nil (default-no-extras invariant)", cmd, got)
		}
	}
}

// TestIsPodLogsCommand pins the I17-F allow-list that gates --follow,
// the duration-string --since widening, the --tail floor description
// suffix, and the diagnostic-extraction path. Pod-logs = apps/logs and
// every services/<type>/{id}/logs; nothing else.
func TestIsPodLogsCommand(t *testing.T) {
	cases := []struct {
		cmdPath string
		want    bool
	}{
		{"apps/logs", true},
		{"services/postgresql/{id}/logs", true},
		{"services/valkey/{id}/logs", true},
		{"services/clickhouse/{id}/logs", true},
		{"services/cert-manager/{id}/logs", true},
		{"services/netbird-server/{id}/logs", true},
		{"services/netbird-client/{id}/logs", true},
		// Endpoint placeholder must match: no `{id}/logs` suffix means
		// the manifest shape isn't the per-instance pod-logs reader.
		{"services/postgresql/logs", false},
		{"services/postgresql/{id}/show", false},
		// builds/logs is structurally different (limit-bounded, object
		// envelope), not a pod-logs stream.
		{"builds/logs", false},
		// Defensive: empty + apps siblings + arbitrary garbage.
		{"", false},
		{"apps/list", false},
		{"apps", false},
		{"random/string", false},
	}
	for _, tt := range cases {
		t.Run(tt.cmdPath, func(t *testing.T) {
			if got := isPodLogsCommand(tt.cmdPath); got != tt.want {
				t.Errorf("isPodLogsCommand(%q) = %v, want %v", tt.cmdPath, got, tt.want)
			}
		})
	}
}

// TestDescriptionSuffixFor pins the I11-R help-text suffix map. The
// suffix is appended verbatim to the manifest's flag description, so
// the user sees the k8s API floor warning on `apps logs --tail`'s help
// row. Every other (command, field) pair returns "" so unrelated flags
// aren't accidentally annotated.
func TestDescriptionSuffixFor(t *testing.T) {
	cases := []struct {
		cmdPath   string
		fieldName string
		want      string
	}{
		{"apps/logs", "tail", " (floors to 1 line at the k8s API minimum; pass 100+ for meaningful history)"},
		{"apps/logs", "since", ""},
		{"apps/logs", "id", ""},
		{"apps/list", "tail", ""},
		// I17-F: service-type logs paths inherit the same tail-floor suffix.
		{"services/postgresql/{id}/logs", "tail", " (floors to 1 line at the k8s API minimum; pass 100+ for meaningful history)"},
		{"services/valkey/{id}/logs", "tail", " (floors to 1 line at the k8s API minimum; pass 100+ for meaningful history)"},
		{"services/postgresql/{id}/show", "tail", ""},
		{"builds/logs", "tail", ""},
		{"", "", ""},
	}
	for _, tt := range cases {
		t.Run(tt.cmdPath+"/"+tt.fieldName, func(t *testing.T) {
			got := descriptionSuffixFor(tt.cmdPath, tt.fieldName)
			if got != tt.want {
				t.Errorf("descriptionSuffixFor(%q, %q) = %q, want %q", tt.cmdPath, tt.fieldName, got, tt.want)
			}
		})
	}
}

// TestAliasPathsFor pins the namespace-alias allow-list driving I12-B.
// Currently only `clusters/mcp/{show,update}` get cross-listed under
// `account/mcp/{show,update}`; other paths pass through unchanged.
func TestAliasPathsFor(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"clusters/mcp/show", []string{"account/mcp/show"}},
		{"clusters/mcp/update", []string{"account/mcp/update"}},
		{"clusters/list", nil},
		{"account/users/list", nil},
		{"", nil},
	}
	for _, tt := range cases {
		t.Run(tt.in, func(t *testing.T) {
			got := aliasPathsFor(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("aliasPathsFor(%q) = %v, want %v", tt.in, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("aliasPathsFor(%q)[%d] = %q, want %q", tt.in, i, got[i], tt.want[i])
				}
			}
		})
	}
}
