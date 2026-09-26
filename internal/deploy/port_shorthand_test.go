package deploy

import "testing"

// FCR 753: after a deploy, runos.yaml kept `port:` / `standardHttps:` while
// the server holds servicePortMappings, so apps diff reported drift on the
// yaml deploy had just written. The writeback now records the canonical form.
func TestFoldPortShorthand(t *testing.T) {
	f := false
	cases := []struct {
		name     string
		in       DeployConfig
		wantPort int
		wantTLS  bool
		wantLen  int
	}{
		{"port only", DeployConfig{Port: 8080}, 8080, true, 1},
		{"port and standardHttps false", DeployConfig{Port: 8080, StandardHttps: &f}, 8080, false, 1},
		{"mappings already set win", DeployConfig{Port: 9090, ServicePortMappings: []ServicePortMapping{{Port: 8080}}}, 8080, true, 1},
		{"no ports at all", DeployConfig{}, 0, true, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := c.in
			FoldPortShorthand(&cfg)
			if cfg.Port != 0 || cfg.StandardHttps != nil {
				t.Errorf("shorthand left in place: port=%d standardHttps=%v", cfg.Port, cfg.StandardHttps)
			}
			if len(cfg.ServicePortMappings) != c.wantLen {
				t.Fatalf("mappings = %+v, want %d entries", cfg.ServicePortMappings, c.wantLen)
			}
			if c.wantLen == 1 {
				m := cfg.ServicePortMappings[0]
				tls := m.StandardHttps == nil || *m.StandardHttps
				if m.Port != c.wantPort || tls != c.wantTLS {
					t.Errorf("mapping = {%d %v}, want {%d %v}", m.Port, tls, c.wantPort, c.wantTLS)
				}
			}
		})
	}
}
