package kubeaccess

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestWriteCredentialPrintsAnExecCredentialV1(t *testing.T) {
	var buf bytes.Buffer
	exp := time.Date(2026, 1, 1, 12, 5, 0, 0, time.FixedZone("x", 7200))
	if err := WriteCredential(&buf, Issued{Token: "jwt.a.b", ExpiresAt: exp}); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, buf.String())
	}
	if got["apiVersion"] != "client.authentication.k8s.io/v1" || got["kind"] != "ExecCredential" {
		t.Errorf("header = %v", got)
	}
	st := got["status"].(map[string]any)
	if st["token"] != "jwt.a.b" || st["expirationTimestamp"] != "2026-01-01T10:05:00Z" {
		t.Errorf("status = %v", st)
	}
	if len(st) != 2 || len(got) != 3 {
		t.Errorf("unexpected extra fields: %v", got)
	}
	if !bytes.HasSuffix(buf.Bytes(), []byte("\n")) || bytes.Count(buf.Bytes(), []byte("\n")) != 1 {
		t.Errorf("want one line of JSON, got %q", buf.String())
	}
}

func TestCheckExecInfoAcceptsV1AndNothingElse(t *testing.T) {
	for env, ok := range map[string]bool{
		"": true,
		`{"apiVersion":"client.authentication.k8s.io/v1","kind":"ExecCredential","spec":{"interactive":false}}`: true,
		`{"apiVersion":"client.authentication.k8s.io/v1beta1","kind":"ExecCredential"}`:                         false,
		`not json`: false,
	} {
		if err := CheckExecInfo(env); (err == nil) != ok {
			t.Errorf("CheckExecInfo(%q) = %v, want ok=%v", env, err, ok)
		}
	}
}
