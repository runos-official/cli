package auth

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestGetIDToken(t *testing.T) {
	tests := []struct {
		name         string
		refreshToken string
		apiKey       string
		wantErr      bool
		errContains  string
	}{
		{
			name:         "empty refresh token and empty API key",
			refreshToken: "",
			apiKey:       "",
			wantErr:      true,
			errContains:  "not authenticated",
		},
		{
			name:         "empty refresh token with valid API key",
			refreshToken: "",
			apiKey:       "valid-api-key",
			wantErr:      true,
			errContains:  "not authenticated",
		},
		{
			name:         "valid refresh token with empty API key",
			refreshToken: "valid-refresh-token",
			apiKey:       "",
			wantErr:      true,
			errContains:  "not authenticated",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := GetIDToken(tt.refreshToken, tt.apiKey)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("GetIDToken() error = nil, want error containing %q", tt.errContains)
				}
				if tt.errContains != "" && err.Error() != tt.errContains {
					t.Errorf("GetIDToken() error = %q, want %q", err.Error(), tt.errContains)
				}
				if got != "" {
					t.Errorf("GetIDToken() returned %q, want empty string on error", got)
				}
			} else {
				if err != nil {
					t.Fatalf("GetIDToken() unexpected error: %v", err)
				}
			}
		})
	}
}

// TestExtractFirebaseUID pins the JWT-payload decoding used to default
// `runos user permissions <uid>` to the session's own Firebase uid
// (I12-D). The function reads `user_id` (Firebase's stable claim) and
// falls back to `sub` (standard JWT) when the Firebase claim is absent.
func TestExtractFirebaseUID(t *testing.T) {
	makeJWT := func(payload map[string]any) string {
		body, _ := json.Marshal(payload)
		return "eyJhbGciOiJSUzI1NiJ9." + base64.RawURLEncoding.EncodeToString(body) + ".sig"
	}

	cases := []struct {
		name  string
		token string
		want  string
	}{
		{"user_id wins", makeJWT(map[string]any{"user_id": "uid-123", "sub": "sub-456"}), "uid-123"},
		{"falls back to sub", makeJWT(map[string]any{"sub": "sub-only"}), "sub-only"},
		{"neither claim", makeJWT(map[string]any{"email": "x@y.z"}), ""},
		{"malformed (no dots)", "not-a-jwt", ""},
		{"malformed (only one part)", "headeronly", ""},
		{"malformed (bad base64)", "h.!!!.s", ""},
		{"malformed (non-json payload)", "h." + base64.RawURLEncoding.EncodeToString([]byte("not json")) + ".s", ""},
		{"empty", "", ""},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractFirebaseUID(tt.token)
			if got != tt.want {
				t.Errorf("ExtractFirebaseUID(%q) = %q, want %q", tt.token, got, tt.want)
			}
		})
	}
}
