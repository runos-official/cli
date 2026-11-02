package roidapi

import (
	"errors"
	"testing"

	"github.com/runos-official/cli/internal/config"
)

func TestGatewayURLIsTheAPIURLNotTheIssuer(t *testing.T) {
	t.Setenv("RUNOS_API_URL", "")
	cfg := &config.Config{AuthIssuer: "https://id.example.com/", ConductorURL: "https://api.example.com/"}
	got, err := GatewayURL(cfg)
	if err != nil || got != "https://api.example.com" {
		t.Fatalf("GatewayURL = %q, %v", got, err)
	}
	if base, _ := BaseURL(cfg); base != "https://id.example.com" {
		t.Errorf("BaseURL = %q: the access calls keep their own base", base)
	}
}

func TestGatewayURLFollowsTheOverrideAndFallsBackToTheIssuer(t *testing.T) {
	cfg := &config.Config{AuthIssuer: "https://id.example.com", ConductorURL: "https://api.example.com"}
	t.Setenv("RUNOS_API_URL", "http://localhost:9999")
	if got, _ := GatewayURL(cfg); got != "http://localhost:9999" {
		t.Errorf("override: GatewayURL = %q", got)
	}
	t.Setenv("RUNOS_API_URL", "")
	cfg.ConductorURL = ""
	if got, _ := GatewayURL(cfg); got != "https://id.example.com" {
		t.Errorf("no API URL: GatewayURL = %q", got)
	}
}

func TestGatewayURLNeedsAnIdentityService(t *testing.T) {
	for _, cfg := range []*config.Config{nil, {ConductorURL: "https://api.example.com"}} {
		if _, err := GatewayURL(cfg); !errors.Is(err, ErrNotRoid) {
			t.Errorf("cfg %+v: err = %v, want ErrNotRoid", cfg, err)
		}
	}
}
