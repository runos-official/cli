package auth

import (
	"testing"

	"github.com/runos-official/cli/internal/config"
)

// The requirement these tests come from is Goal 15 Q&A 120: a CLI
// session authenticated only by a PAT must refuse the approval command.
// The defect they exist to prevent is a CLI that refuses ONE PAT shape
// and accepts the other, which is what happens when the refusing command
// and the sending code answer "is this a PAT?" separately.
func TestKindReportsEveryCredentialPath(t *testing.T) {
	cases := []struct {
		name   string
		envPAT string
		cfg    *config.Config
		want   CredentialKind
		isPAT  bool
	}{
		{
			name:   "env PAT wins over everything",
			envPAT: "pat-from-env",
			cfg: &config.Config{
				APIKey:       "pat-on-disk",
				RefreshToken: "refresh",
				Firebase:     &config.FirebaseConfig{APIKey: "fb"},
			},
			want:  CredentialEnvPAT,
			isPAT: true,
		},
		{
			name: "a PAT stored on disk is still a PAT",
			cfg: &config.Config{
				APIKey:       "pat-on-disk",
				RefreshToken: "refresh",
				Firebase:     &config.FirebaseConfig{APIKey: "fb"},
			},
			want:  CredentialStoredPAT,
			isPAT: true,
		},
		{
			name: "a whitespace-only stored PAT is not a credential",
			cfg: &config.Config{
				APIKey:       "   ",
				RefreshToken: "refresh",
				Firebase:     &config.FirebaseConfig{APIKey: "fb"},
			},
			want:  CredentialInteractive,
			isPAT: false,
		},
		{
			name: "an interactive session is the only non-PAT kind",
			cfg: &config.Config{
				RefreshToken: "refresh",
				Firebase:     &config.FirebaseConfig{APIKey: "fb"},
			},
			want:  CredentialInteractive,
			isPAT: false,
		},
		{
			name: "no credential at all",
			cfg:  &config.Config{},
			want: CredentialNone,
		},
		{
			name: "a nil config is not a credential",
			cfg:  nil,
			want: CredentialNone,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if testCase.envPAT != "" {
				t.Setenv(APIKeyEnvVar, testCase.envPAT)
			} else {
				t.Setenv(APIKeyEnvVar, "")
			}
			if got := Kind(testCase.cfg); got != testCase.want {
				t.Fatalf("Kind = %q, want %q", got, testCase.want)
			}
			if got := Kind(testCase.cfg).IsPAT(); got != testCase.isPAT {
				t.Fatalf("IsPAT = %v, want %v", got, testCase.isPAT)
			}
		})
	}
}

// The property that makes the refusal trustworthy: Kind and ResolveToken
// cannot disagree about which credential is in play, because ResolveToken
// switches on Kind. Asserted by checking that every kind ResolveToken
// reports a token for is the kind Kind named, on the same config.
func TestResolveTokenSendsTheCredentialKindNames(t *testing.T) {
	t.Run("env PAT", func(t *testing.T) {
		t.Setenv(APIKeyEnvVar, "  pat-from-env  ")
		cfg := &config.Config{APIKey: "pat-on-disk"}
		if kind := Kind(cfg); kind != CredentialEnvPAT {
			t.Fatalf("Kind = %q", kind)
		}
		token, err := ResolveToken(cfg)
		if err != nil {
			t.Fatalf("ResolveToken: %v", err)
		}
		if token != "pat-from-env" {
			t.Fatalf("token = %q, want the trimmed env PAT", token)
		}
	})

	t.Run("stored PAT", func(t *testing.T) {
		t.Setenv(APIKeyEnvVar, "")
		cfg := &config.Config{APIKey: "  pat-on-disk  ", Firebase: &config.FirebaseConfig{APIKey: "fb"}}
		if kind := Kind(cfg); kind != CredentialStoredPAT {
			t.Fatalf("Kind = %q", kind)
		}
		token, err := ResolveToken(cfg)
		if err != nil {
			t.Fatalf("ResolveToken: %v", err)
		}
		// The load-bearing assertion: a config carrying BOTH a stored PAT
		// and Firebase settings sends the PAT. If this ever sent the
		// Firebase token instead, Kind would be reporting a PAT while the
		// request carried an interactive credential, and the approve
		// command would refuse a session that could legitimately decide.
		if token != "pat-on-disk" {
			t.Fatalf("token = %q, want the stored PAT", token)
		}
	})

	t.Run("no credential", func(t *testing.T) {
		t.Setenv(APIKeyEnvVar, "")
		if _, err := ResolveToken(&config.Config{}); err == nil {
			t.Fatal("expected ResolveToken to refuse with no credential")
		}
	})
}

// UsingAPIKey is deliberately narrower than Kind and this pins the gap,
// so a future caller reaching for it to answer the credential-kind
// question finds a test saying it is the wrong function.
func TestUsingAPIKeyDoesNotSeeAStoredPAT(t *testing.T) {
	t.Setenv(APIKeyEnvVar, "")
	cfg := &config.Config{APIKey: "pat-on-disk"}
	if UsingAPIKey() {
		t.Fatal("UsingAPIKey reads the env var only and must not see a stored PAT")
	}
	if !Kind(cfg).IsPAT() {
		t.Fatal("Kind must see a stored PAT; this is the difference between the two")
	}
}
