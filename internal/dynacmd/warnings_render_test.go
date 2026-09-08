package dynacmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/manifest"
	"github.com/spf13/cobra"
)

// Objective 92 / story 211: conductor returns an advisory as `warnings`,
// an array of plain strings, in the response body of the call that
// creates the condition it warns about. These tests drive the real
// Execute against a stubbed conductor. Every id, name and warning string
// below is a placeholder; nothing here names a real account, service or
// customer.
//
// Two capture shapes are used deliberately:
//   - separate pipes prove the stream SPLIT (advisory on stderr, table on
//     stdout), because the formatter writes to os.Stdout directly;
//   - one shared pipe proves the ORDER, because two buffers cannot be
//     interleaved after the fact.

const (
	warnTestAccountID = "acct1"
	warnTestClusterID = "cluster1"
	warnTestPAT       = "pat-test-token"
	warnTestServiceID = "svc1"
)

// vllmUpdateCmd is the manifest-driven command the objective actually
// cares about: a vLLM update whose response may carry an advisory about
// a served model name that is not hub-shaped. Object output.
var vllmUpdateCmd = manifest.Command{
	Command:  "services/vllm/{id}/update",
	Endpoint: "/:aid/:cid/services/vllm/:id",
	Method:   http.MethodPatch,
	Input: &manifest.Input{Fields: []manifest.Field{
		{Name: "id", Type: "string", Positional: true, Required: true},
		{Name: "servedModelName", Type: "string"},
	}},
	Output: &manifest.Output{Type: "object", Fields: []manifest.OutputField{
		{Name: "id"}, {Name: "servedModelName"},
	}},
}

// vllmAddCmd returns a job, so it exercises the --follow ordering.
var vllmAddCmd = manifest.Command{
	Command:  "services/vllm/add",
	Endpoint: "/:aid/:cid/services/vllm",
	Method:   http.MethodPost,
	Input: &manifest.Input{Fields: []manifest.Field{
		{Name: "name", Type: "string", Required: true},
	}},
	Output: &manifest.Output{Type: "object", Fields: []manifest.OutputField{
		{Name: "jobId"}, {Name: "osid"},
	}},
}

// vllmListCmd is an array-output command, the shape whose envelope
// unwrap used to discard a sibling `warnings` key outright.
var vllmListCmd = manifest.Command{
	Command:  "services/vllm/list",
	Endpoint: "/:aid/:cid/services/vllm",
	Method:   http.MethodGet,
	Output: &manifest.Output{Type: "array", Fields: []manifest.OutputField{
		{Name: "id"}, {Name: "name"},
	}},
}

// warnStub answers every request with the given status and body.
func warnStub(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// warnEnv points the CLI at a throwaway home carrying a PAT config, and
// clears every environment variable the config getters prefer, so no
// test depends on a developer's config file or shell. Nothing here
// reaches the network beyond the caller's own httptest server.
func warnEnv(t *testing.T, apiURL string) {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, ".runos")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	cfg := fmt.Sprintf(
		`{"account_id":%q,"default_cluster_id":%q,"api_key":%q,"conductor_url":%q}`,
		warnTestAccountID, warnTestClusterID, warnTestPAT, apiURL,
	)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("RUNOS_API_KEY", "")
	t.Setenv("RUNOS_ACCOUNT_ID", "")
	t.Setenv("RUNOS_CLUSTER_ID", "")
	t.Setenv("RUNOS_API_URL", "")
}

// warnCmd builds the cobra command Execute reads its flags from, with
// the same flag names the builder registers.
func warnCmd(t *testing.T, cmdDef manifest.Command, jsonOutput, follow bool) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().BoolP("json", "j", jsonOutput, "Output as JSON")
	cmd.Flags().StringP("file", "f", "", "YAML file with input values")
	cmd.Flags().String("cid", "", "Cluster ID")
	cmd.Flags().Bool("follow", follow, "Follow job progress until completion")
	if cmdDef.Input != nil {
		for _, f := range cmdDef.Input.Fields {
			if f.Positional {
				continue
			}
			cmd.Flags().String(flagNameFor(f.Name), "", f.Description)
		}
	}
	return cmd
}

// captureSplit redirects os.Stdout and os.Stderr into two separate
// pipes. The returned read func restores both and returns what each
// received. The formatter prints with fmt.Println (os.Stdout) and the
// advisory prints to cmd.ErrOrStderr(), which resolves to os.Stderr when
// the cobra command has no explicit writer, so redirecting the process
// streams is what puts both under test at once.
func captureSplit(t *testing.T) func() (stdout, stderr string) {
	t.Helper()
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	prevOut, prevErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, errW

	var outBuf, errBuf bytes.Buffer
	outDone, errDone := make(chan struct{}), make(chan struct{})
	go func() { _, _ = io.Copy(&outBuf, outR); close(outDone) }()
	go func() { _, _ = io.Copy(&errBuf, errR); close(errDone) }()

	restored := false
	restore := func() {
		if restored {
			return
		}
		restored = true
		_ = outW.Close()
		_ = errW.Close()
		<-outDone
		<-errDone
		os.Stdout, os.Stderr = prevOut, prevErr
	}
	t.Cleanup(restore)
	return func() (string, string) {
		restore()
		return outBuf.String(), errBuf.String()
	}
}

// captureCombined sends os.Stdout and os.Stderr down ONE pipe, so the
// order the two streams were written in survives into the assertion.
func captureCombined(t *testing.T) func() string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	prevOut, prevErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = w, w

	var buf bytes.Buffer
	done := make(chan struct{})
	go func() { _, _ = io.Copy(&buf, r); close(done) }()

	restored := false
	restore := func() {
		if restored {
			return
		}
		restored = true
		_ = w.Close()
		<-done
		os.Stdout, os.Stderr = prevOut, prevErr
	}
	t.Cleanup(restore)
	return func() string {
		restore()
		return buf.String()
	}
}

// --- Criterion 1: object output ------------------------------------------

func TestExecute_ObjectOutput_WarningsOnStderrAndNotInTable(t *testing.T) {
	body := `{"id":"svc1","servedModelName":"local-alias","warnings":["first advisory","second advisory"]}`
	srv := warnStub(t, http.StatusOK, body)
	warnEnv(t, localhostURL(srv.URL))

	cmd := warnCmd(t, vllmUpdateCmd, false, false)
	_ = cmd.Flags().Set(flagNameFor("servedModelName"), "local-alias")
	exec := NewExecutor(localhostURL(srv.URL))

	read := captureSplit(t)
	if err := exec.Execute(cmd, []string{warnTestServiceID}, vllmUpdateCmd); err != nil {
		read()
		t.Fatalf("Execute: %v", err)
	}
	stdout, stderr := read()

	wantErr := "Warning: first advisory\nWarning: second advisory\n"
	if stderr != wantErr {
		t.Errorf("stderr = %q, want exactly %q", stderr, wantErr)
	}
	if !strings.Contains(stdout, "servedModelName: local-alias") {
		t.Errorf("stdout lost the normal table: %q", stdout)
	}
	if strings.Contains(stdout, "warnings") {
		t.Errorf("the warnings key must not render as a table row, stdout = %q", stdout)
	}
	if strings.Contains(stdout, "Warning:") {
		t.Errorf("advisory text leaked onto stdout: %q", stdout)
	}
}

// The ordering half of criterion 1: both advisory lines are complete
// before the table is written.
func TestExecute_ObjectOutput_WarningsPrecedeTheTable(t *testing.T) {
	body := `{"id":"svc1","servedModelName":"local-alias","warnings":["first advisory","second advisory"]}`
	srv := warnStub(t, http.StatusOK, body)
	warnEnv(t, localhostURL(srv.URL))

	cmd := warnCmd(t, vllmUpdateCmd, false, false)
	_ = cmd.Flags().Set(flagNameFor("servedModelName"), "local-alias")
	exec := NewExecutor(localhostURL(srv.URL))

	read := captureCombined(t)
	if err := exec.Execute(cmd, []string{warnTestServiceID}, vllmUpdateCmd); err != nil {
		read()
		t.Fatalf("Execute: %v", err)
	}
	combined := read()

	lastWarning := strings.Index(combined, "Warning: second advisory")
	firstTableRow := strings.Index(combined, "id ")
	if lastWarning < 0 || firstTableRow < 0 {
		t.Fatalf("expected both the advisories and the table, got %q", combined)
	}
	if lastWarning > firstTableRow {
		t.Errorf("advisories must be complete before the table starts, got %q", combined)
	}
}

// --- Criterion 2: --json is structurally untouched ------------------------

func TestExecute_JSONOutput_KeepsWarningsInTheBody(t *testing.T) {
	body := `{"id":"svc1","servedModelName":"local-alias","warnings":["first advisory","second advisory"]}`
	srv := warnStub(t, http.StatusOK, body)
	warnEnv(t, localhostURL(srv.URL))

	cmd := warnCmd(t, vllmUpdateCmd, true, false)
	_ = cmd.Flags().Set(flagNameFor("servedModelName"), "local-alias")
	exec := NewExecutor(localhostURL(srv.URL))

	read := captureSplit(t)
	if err := exec.Execute(cmd, []string{warnTestServiceID}, vllmUpdateCmd); err != nil {
		read()
		t.Fatalf("Execute: %v", err)
	}
	stdout, stderr := read()

	var decoded map[string]any
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("stdout must parse as JSON, got %q (%v)", stdout, err)
	}
	warnings, ok := decoded["warnings"].([]any)
	if !ok || len(warnings) != 2 {
		t.Errorf("--json must keep the warnings key intact, got %v", decoded["warnings"])
	}
	if strings.Contains(stdout, "Warning:") {
		t.Errorf("no warning text may be interleaved into the machine-readable stream: %q", stdout)
	}
	// The advisory still reaches an operator running --json in a
	// terminal; stderr is not the machine-readable stream (D5).
	if !strings.Contains(stderr, "Warning: first advisory") {
		t.Errorf("stderr should still carry the advisory under --json, got %q", stderr)
	}
}

// --- Criterion 3: array output --------------------------------------------

func TestExecute_ArrayOutput_EnvelopeNoLongerSwallowsWarnings(t *testing.T) {
	body := `{"items":[{"id":"svc1","name":"lane-one"},{"id":"svc2","name":"lane-two"}],"warnings":["envelope advisory"]}`
	srv := warnStub(t, http.StatusOK, body)
	warnEnv(t, localhostURL(srv.URL))

	cmd := warnCmd(t, vllmListCmd, false, false)
	exec := NewExecutor(localhostURL(srv.URL))

	read := captureSplit(t)
	if err := exec.Execute(cmd, nil, vllmListCmd); err != nil {
		read()
		t.Fatalf("Execute: %v", err)
	}
	stdout, stderr := read()

	if stderr != "Warning: envelope advisory\n" {
		t.Errorf("stderr = %q, want the single advisory line", stderr)
	}
	for _, want := range []string{"lane-one", "lane-two"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lost a table row (%s): %q", want, stdout)
		}
	}
	if strings.Contains(stdout, "envelope advisory") {
		t.Errorf("advisory text must not also appear on stdout: %q", stdout)
	}
}

// --- Criterion 4: --follow ------------------------------------------------

// The only job-progress mode in this repo is --follow (there is no
// --wait flag). The advisory must land before the first progress line,
// not after the job terminates.
func TestExecute_Follow_WarningsPrecedeJobProgress(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/jobs/") && strings.HasSuffix(r.URL.Path, "/work-items"):
			fmt.Fprint(w, `{"workItems":[]}`)
		case strings.Contains(r.URL.Path, "/jobs/"):
			fmt.Fprint(w, `{"jobId":"job-1","status":"completed","type":"createService"}`)
		default:
			fmt.Fprint(w, `{"jobId":"job-1","osid":"osid-1","warnings":["follow advisory"]}`)
		}
	}))
	t.Cleanup(srv.Close)

	warnEnv(t, localhostURL(srv.URL))
	t.Setenv("RUNOS_API_URL", localhostURL(srv.URL))

	cmd := warnCmd(t, vllmAddCmd, false, true)
	_ = cmd.Flags().Set("name", "lane-one")
	exec := NewExecutor(localhostURL(srv.URL))

	read := captureCombined(t)
	err := exec.Execute(cmd, nil, vllmAddCmd)
	combined := read()
	if err != nil {
		t.Fatalf("Execute: %v (output %q)", err, combined)
	}

	warnAt := strings.Index(combined, "Warning: follow advisory")
	if warnAt < 0 {
		t.Fatalf("advisory never printed, output was %q", combined)
	}
	// renderFollowResponse prints the body first, then followJob scrolls
	// progress over it. The advisory must precede BOTH.
	bodyAt := strings.Index(combined, "jobId")
	progressAt := strings.Index(combined, "completed")
	if bodyAt < 0 || progressAt < 0 {
		t.Fatalf("expected the rendered response and the job progress, output was %q", combined)
	}
	if warnAt > bodyAt || warnAt > progressAt {
		t.Errorf("advisory must precede the rendered body and the first job-progress line, got %q", combined)
	}
	if strings.Contains(combined, "warnings ") {
		t.Errorf("the warnings key must not render as a table row under --follow: %q", combined)
	}
}

// --- Criterion 6: a response with no warnings key is untouched ------------

func TestExecute_NoWarningsKey_OutputUnchanged(t *testing.T) {
	t.Run("object output", func(t *testing.T) {
		srv := warnStub(t, http.StatusOK, `{"id":"svc1","servedModelName":"local-alias"}`)
		warnEnv(t, localhostURL(srv.URL))

		cmd := warnCmd(t, vllmUpdateCmd, false, false)
		_ = cmd.Flags().Set(flagNameFor("servedModelName"), "local-alias")
		exec := NewExecutor(localhostURL(srv.URL))

		read := captureSplit(t)
		if err := exec.Execute(cmd, []string{warnTestServiceID}, vllmUpdateCmd); err != nil {
			read()
			t.Fatalf("Execute: %v", err)
		}
		stdout, stderr := read()

		// Byte-identical baseline, captured from the pre-change build.
		wantOut := "id             : svc1\nservedModelName: local-alias\n"
		if stdout != wantOut {
			t.Errorf("stdout = %q, want %q", stdout, wantOut)
		}
		if stderr != "" {
			t.Errorf("stderr = %q, want nothing", stderr)
		}
	})

	t.Run("array output", func(t *testing.T) {
		srv := warnStub(t, http.StatusOK, `{"items":[{"id":"svc1","name":"lane-one"}]}`)
		warnEnv(t, localhostURL(srv.URL))

		cmd := warnCmd(t, vllmListCmd, false, false)
		exec := NewExecutor(localhostURL(srv.URL))

		read := captureSplit(t)
		if err := exec.Execute(cmd, nil, vllmListCmd); err != nil {
			read()
			t.Fatalf("Execute: %v", err)
		}
		stdout, stderr := read()

		wantOut := "ID    NAME      \n----------------\nsvc1  lane-one  \n"
		if stdout != wantOut {
			t.Errorf("stdout = %q, want %q", stdout, wantOut)
		}
		if stderr != "" {
			t.Errorf("stderr = %q, want nothing", stderr)
		}
	})
}

// --- Criterion 8: non-success responses are unaffected --------------------

func TestExecute_ErrorResponse_EmitsNoWarningLine(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"plain error envelope", `{"error":"served model name is required"}`},
		{"error envelope carrying warnings", `{"error":"served model name is required","warnings":["should not print"]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := warnStub(t, http.StatusBadRequest, tc.body)
			warnEnv(t, localhostURL(srv.URL))

			cmd := warnCmd(t, vllmUpdateCmd, false, false)
			_ = cmd.Flags().Set(flagNameFor("servedModelName"), "local-alias")
			exec := NewExecutor(localhostURL(srv.URL))

			read := captureSplit(t)
			err := exec.Execute(cmd, []string{warnTestServiceID}, vllmUpdateCmd)
			stdout, stderr := read()

			if err == nil {
				t.Fatalf("expected the 400 to surface as an error, stdout %q", stdout)
			}
			if !strings.Contains(err.Error(), "served model name is required") {
				t.Errorf("error envelope rendering changed: %v", err)
			}
			if strings.Contains(stderr, "Warning:") || strings.Contains(stdout, "Warning:") {
				t.Errorf("no advisory line may accompany an error (stdout %q, stderr %q)", stdout, stderr)
			}
		})
	}
}

// --- Criterion 5: the singular key stays the banner's ---------------------

// `account api-keys add` returns a one-shot token and a SINGULAR
// `warning` string, which printApiKeyTokenBanner already renders under
// the table. The generic PLURAL renderer must not touch it, so this
// command's output is byte-identical to what it was before story 211.
//
// The story's criterion 5 phrased the check as "exactly one occurrence
// of its warning text". That premise does not hold in either build: the
// banner deliberately repeats the text under the table row, so today's
// output carries it twice. Byte-identity against the pre-change build is
// the load-bearing half of the criterion, and it is what is asserted
// here; the literal below was captured by running this same test against
// the executor with the story-211 hunk reverted.
func TestExecute_SingularWarning_NotReprinted(t *testing.T) {
	const bannerText = "This token is shown once and cannot be retrieved again."
	apiKeysAdd := manifest.Command{
		Command:  "account/api-keys/add",
		Endpoint: "/:aid/api-keys",
		Method:   http.MethodPost,
		Input: &manifest.Input{Fields: []manifest.Field{
			{Name: "name", Type: "string", Required: true},
		}},
		Output: &manifest.Output{Type: "object", Fields: []manifest.OutputField{
			{Name: "id"}, {Name: "name"}, {Name: "token"}, {Name: "warning"},
		}},
	}
	body := fmt.Sprintf(`{"id":"key1","name":"ci","token":"placeholder-token","warning":%q}`, bannerText)
	srv := warnStub(t, http.StatusOK, body)
	warnEnv(t, localhostURL(srv.URL))

	cmd := warnCmd(t, apiKeysAdd, false, false)
	_ = cmd.Flags().Set("name", "ci")
	exec := NewExecutor(localhostURL(srv.URL))

	read := captureSplit(t)
	if err := exec.Execute(cmd, nil, apiKeysAdd); err != nil {
		read()
		t.Fatalf("Execute: %v", err)
	}
	stdout, stderr := read()

	wantOut := "id     : key1\n" +
		"name   : ci\n" +
		"token  : placeholder-token\n" +
		"warning: " + bannerText + "\n" +
		"\n" +
		"============================================================\n" +
		"  ONE-SHOT TOKEN - save it now, it cannot be retrieved\n" +
		"============================================================\n" +
		"\n" +
		"  placeholder-token\n" +
		"\n" +
		"  " + bannerText + "\n" +
		"============================================================\n"
	if stdout != wantOut {
		t.Errorf("stdout = %q, want the pre-change build's bytes %q", stdout, wantOut)
	}
	if stderr != "" {
		t.Errorf("the generic plural renderer must stay silent for a singular warning, stderr = %q", stderr)
	}
}

// --- Criterion 7: the deploy command cannot double-print ------------------

// The served manifest DOES carry a `deploy` entry declaring a `warnings`
// output field, so the only thing standing between the static deploy
// path's own `Warning:` loop and story 211's generic one is that
// cmd/root.go hands deployCmd to the builder as an existing command.
// buildCommandTree then merges into it and never creates a dynacmd leaf,
// so Execute is never reached for `runos deploy`. Pinned here because
// dropping deployCmd from WithExistingCommands would silently start
// printing every deploy advisory twice.
func TestBuilder_DeployStaysStaticAndNeverReachesExecute(t *testing.T) {
	deployEntry := manifest.Command{
		Command:  "deploy",
		Endpoint: "/:aid/:cid/deploy",
		Method:   http.MethodPost,
		Output: &manifest.Output{Type: "object", Fields: []manifest.OutputField{
			{Name: "jobId"}, {Name: "osid"}, {Name: "warnings"},
		}},
	}
	staticDeploy := &cobra.Command{
		Use: "deploy",
		RunE: func(*cobra.Command, []string) error {
			return nil
		},
	}
	staticRunE := staticDeploy.RunE

	b := NewBuilder(&manifest.Manifest{
		Version:  "test",
		Commands: []manifest.Command{deployEntry, vllmListCmd},
	}, NewExecutor("http://localhost:0")).WithExistingCommands(staticDeploy)

	for _, top := range b.BuildCommands() {
		if top.Name() == "deploy" {
			t.Fatalf("builder produced a dynamic `deploy` command; the static one must own the name")
		}
	}
	if len(staticDeploy.Commands()) != 0 {
		t.Errorf("builder attached %d subcommand(s) to the static deploy command", len(staticDeploy.Commands()))
	}
	if fmt.Sprintf("%p", staticDeploy.RunE) != fmt.Sprintf("%p", staticRunE) {
		t.Errorf("builder replaced the static deploy RunE, which would route deploy through Execute")
	}
}

// --- The commands that already declare `warnings` -------------------------

// Seven commands in the served manifest (version 45.5.0) already declare
// `warnings` as an output field: apps/add, apps/update,
// services/umami/add, services/umami/{id}/update,
// nodes/configure-gpu-shape, storage-groups/delete, and deploy (which is
// static, see TestBuilder_DeployStaysStaticAndNeverReachesExecute). For
// the six manifest-driven ones the advisory used to render as a declared
// table row; it now prints as warning lines instead, and the row is
// suppressed so the text is not shown twice. That is a deliberate
// behaviour change to live commands, so it is asserted rather than left
// to the generic case.
func TestExecute_DeclaredWarningsField_RendersAsWarningsNotAsARow(t *testing.T) {
	appsAdd := manifest.Command{
		Command:  "apps/add",
		Endpoint: "/:aid/:cid/apps",
		Method:   http.MethodPost,
		Input: &manifest.Input{Fields: []manifest.Field{
			{Name: "name", Type: "string", Required: true},
		}},
		Output: &manifest.Output{Type: "object", Fields: []manifest.OutputField{
			{Name: "jobId"}, {Name: "id"}, {Name: "osid"}, {Name: "warnings"},
		}},
	}
	body := `{"jobId":"job-1","id":"app1","osid":"osid-1","warnings":["declared advisory"]}`
	srv := warnStub(t, http.StatusOK, body)
	warnEnv(t, localhostURL(srv.URL))

	cmd := warnCmd(t, appsAdd, false, false)
	_ = cmd.Flags().Set("name", "lane-one")
	exec := NewExecutor(localhostURL(srv.URL))

	read := captureSplit(t)
	if err := exec.Execute(cmd, nil, appsAdd); err != nil {
		read()
		t.Fatalf("Execute: %v", err)
	}
	stdout, stderr := read()

	if stderr != "Warning: declared advisory\n" {
		t.Errorf("stderr = %q, want the advisory line", stderr)
	}
	if strings.Contains(stdout, "declared advisory") {
		t.Errorf("the declared warnings row must be suppressed so the text is not shown twice: %q", stdout)
	}
	for _, want := range []string{"job-1", "app1", "osid-1"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lost a declared field (%s): %q", want, stdout)
		}
	}
}

// A mixed array is the case the plan's information-loss risk was really
// about: an entry that is not a string prints no line, so the key is
// KEPT and the existing renderer still shows what went unprinted. The
// operator never sees `map[...]` and never loses an entry.
func TestExecute_MixedWarningsArray_PrintsStringsAndKeepsTheKey(t *testing.T) {
	body := `{"id":"svc1","servedModelName":"local-alias","warnings":["a string advisory",{"text":"structured"}]}`
	srv := warnStub(t, http.StatusOK, body)
	warnEnv(t, localhostURL(srv.URL))

	cmd := warnCmd(t, vllmUpdateCmd, false, false)
	_ = cmd.Flags().Set(flagNameFor("servedModelName"), "local-alias")
	exec := NewExecutor(localhostURL(srv.URL))

	read := captureSplit(t)
	if err := exec.Execute(cmd, []string{warnTestServiceID}, vllmUpdateCmd); err != nil {
		read()
		t.Fatalf("Execute: %v", err)
	}
	stdout, stderr := read()

	if stderr != "Warning: a string advisory\n" {
		t.Errorf("stderr = %q, want only the string entry", stderr)
	}
	if !strings.Contains(stdout, "warnings") {
		t.Errorf("an incompletely printed array must keep its key so nothing is lost: %q", stdout)
	}
	if strings.Contains(stdout, "map[") {
		t.Errorf("a non-string entry must never reach the operator as a Go map literal: %q", stdout)
	}
}
