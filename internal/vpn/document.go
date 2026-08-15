package vpn

// The desired-state document Conductor serves at GET /:aid/vpn/devices/:id/state: one read, one
// shape, the whole truth about what this device should have. Field names mirror the JSON exactly;
// the daemon never reinterprets them, it converges to them (see plan.go).

// Document is the device's desired state.
type Document struct {
	Device struct {
		ID      string `json:"id"`
		Address string `json:"address"`
		Session struct {
			ExpiresAt     string `json:"expiresAt"`
			LoginRequired bool   `json:"loginRequired"`
		} `json:"session"`
	} `json:"device"`
	Clusters []DocumentCluster `json:"clusters"`
	Revision string            `json:"revision"`
}

// DocumentCluster is one cluster of the account, connected or not.
type DocumentCluster struct {
	CID       string `json:"cid"`
	Name      string `json:"name"`
	Connected bool   `json:"connected"`
	Server    struct {
		PublicKey string `json:"publicKey"`
		Endpoint  string `json:"endpoint"`
		Reachable bool   `json:"reachable"`
		Reason    string `json:"reason"`
	} `json:"server"`
	AllowedIPs []string `json:"allowedIps"`
	DNS        struct {
		Resolver string   `json:"resolver"`
		Zones    []string `json:"zones"`
	} `json:"dns"`
	PeeredWith []string `json:"peeredWith"`
}
