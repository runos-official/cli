package apps

import "gopkg.in/yaml.v3"

// portShorthand is the legacy top-level `port:` / `standardHttps:` pair
// that runos deploy accepts as sugar for one servicePortMappings entry.
type portShorthand struct {
	Port          int   `yaml:"port"`
	StandardHttps *bool `yaml:"standardHttps"`
}

// applyPortShorthand folds the legacy shorthand into app.ServicePortMappings
// when the yaml declares no mappings, with the rule the conductor applies on
// deploy: an explicit mappings list wins; a scalar port becomes one mapping
// whose standardHttps defaults to true. Without it, apps sync read a
// shorthand yaml as "no ports" and planned servicePortMappings: [] (FCR 753).
func applyPortShorthand(app *PulledApp, data []byte) {
	if len(app.ServicePortMappings) > 0 {
		return
	}
	var s portShorthand
	if err := yaml.Unmarshal(data, &s); err != nil || s.Port == 0 {
		return
	}
	app.ServicePortMappings = []Port{{Port: s.Port, StandardHttps: s.StandardHttps}}
}
