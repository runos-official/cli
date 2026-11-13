package cmd

import (
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/runos-official/cli/internal/config"
)

// issuerRefreshTimeout bounds the CDN read, so a slow CDN costs a sign-in a few seconds and no more.
const issuerRefreshTimeout = 4 * time.Second

// fetchCDNConfig reads the CDN environment config. A var so tests stay network-free.
var fetchCDNConfig = config.FetchRemoteConfigWithin

// cdnCache holds the one CDN read of this process, a failure included, so the beta move and the
// issuer refresh never cost a sign-in two reads.
var cdnCache struct {
	mu   sync.Mutex
	done bool
	rc   *config.RemoteConfig
	err  error
}

func cdnConfig() (*config.RemoteConfig, error) {
	cdnCache.mu.Lock()
	defer cdnCache.mu.Unlock()
	if !cdnCache.done {
		cdnCache.rc, cdnCache.err = fetchCDNConfig(issuerRefreshTimeout)
		cdnCache.done = true
	}
	return cdnCache.rc, cdnCache.err
}

func resetCDNConfigCache() {
	cdnCache.mu.Lock()
	defer cdnCache.mu.Unlock()
	cdnCache.done, cdnCache.rc, cdnCache.err = false, nil, nil
}

// couldBeCDNEntry is the cheap test before a CDN read: a CDN entry is served over https from a
// DNS name. A loopback host, an IP literal or plain http cannot be one, so a local development
// config never pays a read. The decision itself is the exact comparison with the CDN's entry.
func couldBeCDNEntry(cfg *config.Config) bool {
	for _, raw := range []string{cfg.ConsoleURL, cfg.ConductorURL} {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" {
			return false
		}
		host := strings.ToLower(u.Hostname())
		if host == "" || !strings.Contains(host, ".") || net.ParseIP(host) != nil || strings.HasSuffix(host, ".localhost") {
			return false
		}
	}
	return true
}

// cdnEnvironmentOf names the CDN environment whose console and API URLs are exactly the config's,
// or "" when there is none or more than one.
func cdnEnvironmentOf(cfg *config.Config, rc *config.RemoteConfig) string {
	name := ""
	for candidate, env := range rc.Environments {
		if env.Domains.Console != cfg.ConsoleURL || env.Domains.APIURL() != cfg.ConductorURL {
			continue
		}
		if name != "" {
			return ""
		}
		name = candidate
	}
	return name
}

/*
refreshEmptyAuthIssuer fills an empty sign-in issuer from the CDN config, once, at sign-in.

The CLI stamps the issuer into config.json only when an environment is applied. A config made
before the environment named an issuer kept an empty one for ever. This reads the environment's
current entry and stores the issuer it names, then the caller goes on to the normal roid check.

The environment is the config's own label when the label is true. A config with no label (an old
one) or a label its URLs no longer match is the CDN environment whose console and API URLs it holds
exactly, when exactly one does; the label is stamped then. A config whose URLs equal no CDN entry is
custom and is left alone.

It does nothing when an issuer is already stored (never replaced), when RUNOS_API_URL is set, when
the CDN cannot be read, or when the environment names no valid issuer. None of those is an error:
the sign-in goes on exactly as before. When it fills the issuer it says so once.
*/
func refreshEmptyAuthIssuer(cfg *config.Config, report signInReporter, notice io.Writer) {
	if cfg.AuthIssuer != "" || os.Getenv("RUNOS_API_URL") != "" {
		return
	}
	labelled := cfg.GetEnv() != config.EnvCustom
	if !labelled && !couldBeCDNEntry(cfg) {
		return
	}
	rc, err := cdnConfig()
	if err != nil || rc == nil {
		return
	}
	name := cfg.Env
	if !labelled {
		name = cdnEnvironmentOf(cfg, rc)
	}
	env, ok := rc.Environments[name]
	if !ok || name == retiredEnvironment {
		return
	}
	before := cfg.Clone()
	if !labelled {
		cfg.ApplyEnvironment(name, env.Domains.Console, env.Domains.APIURL())
	}
	if err := cfg.ApplyEnvironmentAuth(env.Auth); err != nil || cfg.AuthIssuer == "" {
		*cfg = before
		return
	}
	if err := cfg.Save(); err != nil {
		*cfg = before
		return
	}
	message := fmt.Sprintf("This sign-in now goes through the RunOS identity service at %s.", issuerHost(cfg.AuthIssuer))
	fmt.Fprintln(notice, "Note: "+message)
	if n, ok := report.(signInNoticer); ok {
		n.Notice("identity_service_enabled", message)
	}
}

func issuerHost(issuer string) string {
	if u, err := url.Parse(issuer); err == nil && u.Host != "" {
		return u.Host
	}
	return issuer
}
