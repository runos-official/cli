package auth

import "testing"

// Prefixes never establish Type: DescribeSource builds the zero-type
// Description for every secret source, because Kind alone (the source)
// never carries verified semantics. This is the behavior that keeps a
// PAT-labelled env secret from becoming a false "Personal Access Token"
// claim, and the same path an Account API Key takes.
func TestDescribeSourceNeverGuessesType(t *testing.T) {
	for _, source := range []CredentialKind{
		CredentialEnvKey, CredentialStoredKey, CredentialRoid, CredentialInteractive, CredentialNone,
	} {
		d := DescribeSource(source)
		if d.Source != source {
			t.Fatalf("DescribeSource(%q).Source = %q", source, d.Source)
		}
		if d.Type != CredentialTypeUnknown {
			t.Fatalf("DescribeSource(%q).Type = %q, want unknown", source, d.Type)
		}
	}
}

func TestDescriptionIsSecretCredentialMatchesSource(t *testing.T) {
	cases := []struct {
		source CredentialKind
		want   bool
	}{
		{CredentialEnvKey, true},
		{CredentialStoredKey, true},
		{CredentialRoid, false},
		{CredentialInteractive, false},
		{CredentialNone, false},
	}
	for _, c := range cases {
		d := DescribeSource(c.source)
		if got := d.IsSecretCredential(); got != c.want {
			t.Fatalf("DescribeSource(%q).IsSecretCredential() = %v, want %v", c.source, got, c.want)
		}
	}
}

// Label must never claim a specific semantic type ("Personal Access
// Token", "Account API Key") unless Type was actually set from a
// trusted response. An unknown-type secret gets the generic label.
func TestDescriptionLabel(t *testing.T) {
	cases := []struct {
		name string
		desc Description
		want string
	}{
		{"unverified env secret", DescribeSource(CredentialEnvKey), "API credential"},
		{"unverified stored secret", DescribeSource(CredentialStoredKey), "API credential"},
		{"roid session", DescribeSource(CredentialRoid), "signed-in session"},
		{"interactive session", DescribeSource(CredentialInteractive), "signed-in session"},
		{"no credential", DescribeSource(CredentialNone), "credential"},
		{
			"verified personal PAT",
			Description{Source: CredentialStoredKey, Type: CredentialTypePersonalPAT},
			"Personal Access Token",
		},
		{
			"verified account API key",
			Description{Source: CredentialEnvKey, Type: CredentialTypeAccountAPIKey},
			"Account API Key",
		},
		{
			"legacy with classification",
			Description{Source: CredentialStoredKey, Type: CredentialTypeLegacy, LegacyClassification: "conductor_personal"},
			"legacy credential (conductor_personal)",
		},
		{
			"legacy without classification",
			Description{Source: CredentialStoredKey, Type: CredentialTypeLegacy},
			"legacy credential",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.desc.Label(); got != c.want {
				t.Fatalf("Label() = %q, want %q", got, c.want)
			}
		})
	}
}
