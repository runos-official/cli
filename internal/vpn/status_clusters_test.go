package vpn

import (
	"encoding/json"
	"strings"
	"testing"
)

// RunOS Desktop decodes `clusters` as a list. A null there made it fail to read the whole status,
// so the menu lost its cluster rows and the Restart VPN item (FCR 1730). A status with no cluster
// document applied yet must say `"clusters":[]`, from every code path that encodes it.
func TestStatusEncodesNoClustersAsAnEmptyList(t *testing.T) {
	cases := map[string]Status{
		"nil clusters":   {Running: true},
		"empty clusters": {Running: true, Clusters: []ClusterStatus{}},
	}
	for name, status := range cases {
		t.Run(name, func(t *testing.T) {
			out, err := json.Marshal(status)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(out), `"clusters":[]`) {
				t.Errorf("want \"clusters\":[] in %s", out)
			}
		})
	}
}
