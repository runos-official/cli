package config

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestAPIURLQuietUsesSameSelectionWithoutWarnings(t *testing.T) {
	for _, tc := range []struct {
		name, stored, env, want string
		warning                 bool
	}{
		{"stored", "https://api.example.com", "", "https://api.example.com", false},
		{"override", "https://api.example.com", "https://override.example.com", "https://override.example.com", false},
		{"empty", "", "", "", false},
		{"localhost", "http://localhost:1234", "", "http://localhost:1234", false},
		{"insecure", "http://api.example.com", "", "http://api.example.com", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("RUNOS_API_URL", tc.env)
			cfg := &Config{ConductorURL: tc.stored}
			for _, quiet := range []bool{false, true} {
				reader, writer, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				previous := os.Stderr
				os.Stderr = writer
				var got string
				if quiet {
					got = cfg.GetAPIURLQuiet()
				} else {
					got = cfg.GetAPIURL()
				}
				os.Stderr = previous
				writer.Close()
				body, err := io.ReadAll(reader)
				reader.Close()
				if err != nil {
					t.Fatal(err)
				}
				if got != tc.want {
					t.Fatalf("quiet=%v: URL=%q, want=%q", quiet, got, tc.want)
				}
				if wantWarning := tc.warning && !quiet; strings.Contains(string(body), "Warning:") != wantWarning {
					t.Fatalf("quiet=%v: stderr=%q", quiet, body)
				}
			}
		})
	}
}
