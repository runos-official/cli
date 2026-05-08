# Testing in CLI

## Philosophy

We test for one reason: **catch regressions of bugs we've already fixed**, plus the small handful of pure utilities whose contracts other code relies on. We do not test for coverage metrics, ceremony, or "good practice."

A test earns its place if a future change that re-introduces a known bug would fail it. If a test wouldn't catch any plausible future mistake, it's bloat — delete it.

The order of preference, from highest to lowest value:

1. **Pure-function regression tests** for fixes. A bug landed, we fixed it, the fix lives in a function we can call directly with a few inputs. Cheap to write, cheap to run, near-zero maintenance. Always write these.
2. **Pure-function tests for shared utilities** (e.g. `stripSourceDir`, `computeYAMLPatch`, `computeDriftPatch`) that other code treats as a contract. Worth writing once, even without a specific bug.
3. **Functional tests on extracted helpers.** If a fix lives inside a cobra command handler (e.g. apps_pull's `--keep-env` skip logic) or an MCP tool wrapper (e.g. exit-code-2 → success translation) but the *decision logic* can be lifted into a small helper, do that, then test the helper.
4. **Cobra command-level tests.** Heavyweight (cobra args, exec, stdin/stdout). Generally not worth it. Prefer extracting the interesting bit into a function and testing that.
5. **End-to-end tests against a live conductor / cluster.** Out of scope for now. We get more signal from manual deploys.

## What we do NOT test

- Third-party library behaviour (cobra, viper, yaml.v3, etc.). They have their own tests.
- Trivial getters or pass-through wrappers.
- Subprocess-launching tests when a function-level test would suffice. `os/exec` round-trips are slow and flaky on CI.
- Anything that requires the conductor to be running.

## Conventions

- **Framework:** stdlib `testing` plus `t.Parallel()` where independent. No third-party assertion library — `reflect.DeepEqual` and `t.Errorf` are sufficient.
- **Co-location:** Tests live next to the file under test as `<name>_test.go`. Example: `internal/apps/sync.go` and `internal/apps/sync_test.go`. This is Go's default and matches what's already here.
- **Table tests:** Use `[]struct{name string; ...}` with subtests via `t.Run(tc.name, func(t *testing.T) {...})` whenever there are 2+ cases. Keeps each case isolated and lets you run a single case with `go test -run`.
- **Test names:** Describe the behaviour the test enforces. Good: `TestComputeSyncPlan_ClassOnlySwap_OmitsUnchangedResources`. Bad: `TestSync` or `TestCase1`.
- **Why-comments on regression tests:** When a test exists because of a specific bug, leave a comment naming the bug. Tells the next maintainer whether they can delete the test if the underlying logic moves. Example: `// Regression test for the services_sync class-only swap bug.`

## Patterns

### Pure-function regression test (table form)

```go
func TestStripSourceDir_RemovesLocalOnlyField(t *testing.T) {
    t.Parallel()
    cases := []struct {
        name string
        in   []byte
        want string
    }{
        {
            name: "drops top-level sourceDir",
            in:   []byte("name: foo\nsourceDir: ../..\nport: 3000\n"),
            want: "name: foo\nport: 3000\n",
        },
        // ...
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            got := stripSourceDir(tc.in)
            if string(got) != tc.want {
                t.Errorf("got %q, want %q", got, tc.want)
            }
        })
    }
}
```

### When to extract a pure helper

If the fix lives inside a cobra `RunE` or an MCP handler and the decision logic is non-trivial, lift it out:

```go
// Before: skip logic inlined in apps_pull's RunE
func runAppsPull(cmd *cobra.Command, args []string) error {
    // ... a lot of cobra plumbing ...
    if !keepEnv {
        if err := SaveSecretEnv(...); err != nil { return err }
        if err := SaveEnv(...); err != nil { return err }
    } else {
        fmt.Println("Skipping env writes (--keep-env)")
    }
    // ...
}

// After: skip decision is a pure helper
func ShouldWriteEnvFiles(keepEnv bool) bool { return !keepEnv }
// runAppsPull calls ShouldWriteEnvFiles(keepEnv) and writes accordingly
// test: 2 lines per case, no cobra mock
```

Don't extract just to extract. If the logic is one-line, the helper is bloat.

### MCP exit-code translation

Where MCP wrappers translate a process's exit code (e.g. drift-as-error → success), the translation is a tiny pure function. Test it directly with a synthesized `*exec.ExitError`. Don't shell out.

## When NOT to add a test

- **Doc fixes** (mcp-topic edits, README changes). No test.
- **Manifest field additions** that the conductor handles uniformly. No test.
- **Pure flag plumbing** (adding a `--flag` and threading it to a function that already has tests). The flag itself doesn't need a test.

## Maintaining tests

- If a test starts failing because the underlying behaviour legitimately changed, **update the test** to match the new contract.
- If you change a manifest's allowed-fields list and a sync test breaks, that's the test doing its job — update the fakeManifest to match.
- If you find yourself writing a long mock setup, stop and ask: would lifting the logic out into a pure helper be cheaper? Almost always yes.

## Reference: regression tests by fix

| Fix                                                                                  | Test file                              |
|--------------------------------------------------------------------------------------|----------------------------------------|
| `services_sync` class-only swap flipped class to "custom" (Round 2 Test 16)          | `internal/services/sync_test.go`       |
| `apps_diff` flagged local-only `sourceDir` as drift forever (Round 1 Test 2)         | `internal/apps/diff_test.go`           |
| `apps_sync` rendered preserve-sentinels as drift (Round 1 Test 3a)                   | `internal/apps/sync_test.go`           |
| `apps_pull` rewrote `resourceRequirementClassId: "custom"` as empty (R1 T6)          | `internal/apps/pull_test.go`           |
| `apps_pull --keep-env` flag skips env-file writes (Round 1 follow-up)                | `internal/apps/pull_test.go`           |
| `apps_diff` exit code 2 on drift (Round 1 follow-up)                                 | `internal/apps/diff_test.go`           |
| MCP `apps_diff` exit-code-2 → success translation (Round 1 follow-up)                | `internal/mcp/apps_test.go`            |
| `runos deploy` env-merge flipped to local-wins (Round 1 in-flight)                   | `cmd/deploy_test.go`                   |
| `apps_sync` empty local secret-env wipes server (R5 T1)                              | `internal/apps/sync_test.go`           |
| `apps_sync` override delete leaves local file orphan (R4 T3 #1)                      | `internal/apps/sync_test.go`           |
