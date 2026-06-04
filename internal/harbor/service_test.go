package harbor

import "testing"

// TestResolveUploadURL pins the upload-URL scheme/host validation. The
// upload carries a bearer token, so a relative path resolves against the
// trusted configured base while an absolute URL must be https; http (token
// in clear text) or any other scheme is refused, as is a hostless or empty
// URL.
func TestResolveUploadURL(t *testing.T) {
	const base = "https://api.runos.xyz"

	cases := []struct {
		name      string
		baseURL   string
		uploadURL string
		want      string
		wantErr   bool
	}{
		{"relative resolves against base", base, "/cli-deploy/tok123", "https://api.runos.xyz/cli-deploy/tok123", false},
		{"relative with trailing-slash base", base + "/", "/cli-deploy/tok123", "https://api.runos.xyz/cli-deploy/tok123", false},
		{"absolute https passes through", base, "https://agent.runos.xyz/upload/tok", "https://agent.runos.xyz/upload/tok", false},
		{"absolute http refused", base, "http://agent.runos.xyz/upload/tok", "", true},
		{"absolute ftp refused", base, "ftp://agent.runos.xyz/upload", "", true},
		{"empty refused", base, "", "", true},
		{"whitespace refused", base, "   ", "", true},
		{"https without host refused", base, "https:///upload", "", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveUploadURL(tc.baseURL, tc.uploadURL)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("resolveUploadURL(%q, %q) = %q, want error", tc.baseURL, tc.uploadURL, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveUploadURL(%q, %q) unexpected error: %v", tc.baseURL, tc.uploadURL, err)
			}
			if got != tc.want {
				t.Errorf("resolveUploadURL(%q, %q) = %q, want %q", tc.baseURL, tc.uploadURL, got, tc.want)
			}
		})
	}
}
