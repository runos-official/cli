package dynacmd

import "testing"

// TestResponseHasJobID pins the fix for the `--follow` regression on
// conditionally-async commands: `nodes/delete` returns a jobId only with
// --delete-cloud-instance; the plain delete returns {success, nid} with no job.
// --follow must only engage when a real jobId is present, otherwise a successful
// synchronous response was reported as "response does not contain jobId".
func TestResponseHasJobID(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"async response with jobId", `{"jobId":"abc123","success":true}`, true},
		{"sync delete, no jobId", `{"success":true,"nid":"n1","deleteCloudInstance":false}`, false},
		{"empty jobId string", `{"jobId":""}`, false},
		{"jobId null", `{"jobId":null}`, false},
		{"jobId wrong type", `{"jobId":123}`, false},
		{"invalid json", `not json`, false},
		{"empty body", ``, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := responseHasJobID([]byte(c.body)); got != c.want {
				t.Errorf("responseHasJobID(%q) = %v, want %v", c.body, got, c.want)
			}
		})
	}
}
