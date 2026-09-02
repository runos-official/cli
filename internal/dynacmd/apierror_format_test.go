package dynacmd

import (
	"testing"
)

func TestFormatAuthError(t *testing.T) {
	t.Parallel()
	t.Run("non-401 returns false", func(t *testing.T) {
		_, ok := formatAuthError(&APIError{StatusCode: 500, Body: []byte(`{"error":"boom"}`)}, true)
		if ok {
			t.Error("expected false for non-401")
		}
	})
	t.Run("non-APIError returns false", func(t *testing.T) {
		_, ok := formatAuthError(errString("transport: connection refused"), true)
		if ok {
			t.Error("expected false for non-APIError")
		}
	})
	t.Run("401 on a PAT surfaces the PAT hints", func(t *testing.T) {
		msg, ok := formatAuthError(&APIError{StatusCode: 401, Body: []byte(`{"error":"Invalid token"}`)}, true)
		if !ok {
			t.Fatal("expected ok=true on 401")
		}
		for _, want := range []string{"authentication refused", "Invalid token", "RUNOS_API_KEY", "RUNOS_API_URL", "api-keys list"} {
			if !contains(msg, want) {
				t.Errorf("expected %q in formatted message, got:\n%s", want, msg)
			}
		}
	})
	/*
	 MEASURED ON A LIVE MACHINE, 2026-08-31. An operator on a browser sign-in, pointed at a conductor
	 that would not accept their token, was told to audit `RUNOS_API_KEY` and to run
	 `runos account api-keys list`. They had no PAT. All three lines were about a credential they
	 were not using, and the one command that would have helped was not among them.
	*/
	t.Run("401 on a browser sign-in never mentions a PAT", func(t *testing.T) {
		msg, ok := formatAuthError(&APIError{StatusCode: 401, Body: []byte(`{"error":"Invalid token"}`)}, false)
		if !ok {
			t.Fatal("expected ok=true on 401")
		}
		for _, unwanted := range []string{"RUNOS_API_KEY", "PAT", "api-keys"} {
			if contains(msg, unwanted) {
				t.Errorf("a browser sign-in must not be sent to audit %q, got:\n%s", unwanted, msg)
			}
		}
		if !contains(msg, "runos login") {
			t.Errorf("must name the one command that fixes it, got:\n%s", msg)
		}
		// Conductor's own words survive either way; they are what a diagnosis starts from.
		if !contains(msg, "Invalid token") {
			t.Errorf("must keep conductor's message, got:\n%s", msg)
		}
	})
	t.Run("401 with reason=revoked surfaces the timestamp distinctly", func(t *testing.T) {
		body := []byte(`{"error":"Invalid token","reason":"revoked","revokedAt":"2026-05-12T10:11:12Z"}`)
		msg, ok := formatAuthError(&APIError{StatusCode: 401, Body: body}, true)
		if !ok {
			t.Fatal("expected ok=true on 401")
		}
		for _, want := range []string{"revoked at 2026-05-12T10:11:12Z"} {
			if !contains(msg, want) {
				t.Errorf("expected %q in formatted message, got:\n%s", want, msg)
			}
		}
	})
	t.Run("401 with reason=expired surfaces the timestamp distinctly", func(t *testing.T) {
		body := []byte(`{"error":"Invalid token","reason":"expired","expiredAt":"2026-05-12T10:11:12Z"}`)
		msg, ok := formatAuthError(&APIError{StatusCode: 401, Body: body}, true)
		if !ok {
			t.Fatal("expected ok=true on 401")
		}
		for _, want := range []string{"expired at 2026-05-12T10:11:12Z"} {
			if !contains(msg, want) {
				t.Errorf("expected %q in formatted message, got:\n%s", want, msg)
			}
		}
	})
	t.Run("401 with unparseable body still gets hints", func(t *testing.T) {
		msg, ok := formatAuthError(&APIError{StatusCode: 401, Body: []byte(`not-json`)}, true)
		if !ok {
			t.Fatal("expected ok=true on 401 even when body is not JSON")
		}
		if !contains(msg, "unauthorized") {
			t.Errorf("expected fallback message, got: %s", msg)
		}
	})
}

func TestFormatDependentsError(t *testing.T) {
	t.Parallel()
	t.Run("non-409 returns false", func(t *testing.T) {
		_, ok := formatDependentsError(&APIError{StatusCode: 404, Body: []byte(`{"error":"nope"}`)})
		if ok {
			t.Error("expected false for non-409")
		}
	})
	t.Run("409 with no dependents returns false", func(t *testing.T) {
		body := []byte(`{"error":"some other 409"}`)
		_, ok := formatDependentsError(&APIError{StatusCode: 409, Body: body})
		if ok {
			t.Error("expected false when dependents list is missing")
		}
	})
	t.Run("409 with dependents formats nicely", func(t *testing.T) {
		body := []byte(`{
			"error": "service has dependents",
			"dependents": [
				{"type":"app","id":"abc12","name":"poll-app","alias":"poll-app-db"},
				{"type":"app","id":"def34","name":"auth-svc","alias":"auth-db"}
			]
		}`)
		msg, ok := formatDependentsError(&APIError{StatusCode: 409, Body: body})
		if !ok {
			t.Fatal("expected ok=true when dependents present")
		}
		for _, want := range []string{"service has dependents", "poll-app", "abc12", "poll-app-db", "auth-svc"} {
			if !contains(msg, want) {
				t.Errorf("expected %q in formatted message, got:\n%s", want, msg)
			}
		}
	})
	t.Run("non-APIError returns false", func(t *testing.T) {
		_, ok := formatDependentsError(errString("some other error"))
		if ok {
			t.Error("expected false for non-APIError")
		}
	})
}

// FPL31 D3 and criteria 13/14. A 403 from a module this account switched
// off has to name the module and the command that switches it on; every
// other 403 keeps the rendering it has today.
func TestFormatModuleNotEnabledError(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		err    error
		wantOk bool
	}{
		{
			name:   "the module gate names the enable command",
			err:    &APIError{StatusCode: 403, Body: []byte(`{"error":"Virtual Machines is not enabled","code":"module.not_enabled","module":"virt"}`)},
			wantOk: true,
		},
		{
			name: "a 403 with another code is left alone",
			err:  &APIError{StatusCode: 403, Body: []byte(`{"error":"Admin role required","code":"auth.role_required"}`)},
		},
		{
			name: "a 403 with no code is left alone",
			err:  &APIError{StatusCode: 403, Body: []byte(`{"error":"Forbidden"}`)},
		},
		{
			name: "the code without a module names nothing, so it is left alone",
			err:  &APIError{StatusCode: 403, Body: []byte(`{"error":"nope","code":"module.not_enabled"}`)},
		},
		{
			name: "the code on another status is left alone",
			err:  &APIError{StatusCode: 404, Body: []byte(`{"error":"nope","code":"module.not_enabled","module":"virt"}`)},
		},
		{
			name: "a body that is not JSON is left alone",
			err:  &APIError{StatusCode: 403, Body: []byte(`<html>403</html>`)},
		},
		{
			name: "a transport error is not an APIError",
			err:  errString("dial tcp: connection refused"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			msg, ok := formatModuleNotEnabledError(tc.err)
			if ok != tc.wantOk {
				t.Fatalf("ok = %v, want %v (msg %q)", ok, tc.wantOk, msg)
			}
			if !tc.wantOk {
				return
			}
			// The command is the whole point of the line.
			if !contains(msg, "runos account modules enable virt") {
				t.Errorf("the line does not name the command that fixes it: %s", msg)
			}
			// One line: this is rendered beside cobra's "Error: " prefix.
			if contains(msg, "\n") {
				t.Errorf("the refusal must be one line, got:\n%s", msg)
			}
			for _, want := range []string{"virt", "403", "module.not_enabled"} {
				if !contains(msg, want) {
					t.Errorf("the line omits %q: %s", want, msg)
				}
			}
		})
	}
}
