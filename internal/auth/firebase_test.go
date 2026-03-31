package auth

import (
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
