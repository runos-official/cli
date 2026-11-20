package auth

import (
	"encoding/base64"
	"testing"

	"github.com/runos-official/cli/internal/config"
)

// Measured 2026-11-20 on v1.24.3: `runos login` signed a person in through the
// identity service, and `runos shell devops` then answered "an API key cannot
// open one". The shell read the person's id from the token as a Firebase ID
// token. The identity service token does not carry it there, so the id was
// empty and the shell took the person for an API key.
func TestPersonIDReadsEachSignInKind(t *testing.T) {
	firebaseToken := "h." + base64.RawURLEncoding.EncodeToString([]byte(`{"user_id":"firebase-uid"}`)) + ".s"
	roid := &config.Config{
		AuthIssuer: "https://id.example.test",
		Roid:       &config.RoidSignIn{Issuer: "https://id.example.test", PersonID: "person-1"},
	}
	cases := []struct {
		name   string
		envPAT string
		cfg    *config.Config
		token  string
		want   string
	}{
		{"an identity service sign-in names the person in the config", "", roid, "opaque-access-token", "person-1"},
		{"a Firebase sign-in names the person in the token", "",
			&config.Config{RefreshToken: "r", Firebase: &config.FirebaseConfig{APIKey: "fb"}}, firebaseToken, "firebase-uid"},
		{"a stored API key is not a person", "", &config.Config{APIKey: "pat"}, "pat", ""},
		{"an API key in the environment is not a person, whatever the config holds", "pat", roid, "pat", ""},
		{"no sign-in is not a person", "", &config.Config{}, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(APIKeyEnvVar, tc.envPAT)
			if got := PersonID(tc.cfg, tc.token); got != tc.want {
				t.Fatalf("PersonID = %q, want %q", got, tc.want)
			}
		})
	}
}
