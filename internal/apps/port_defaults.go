package apps

// alignOmittedPortDefaults clears the server's standardHttps on each port
// the local yaml lists without the field, when the server value is the
// platform default (true). An omitted field and the default describe the
// same app, so the yaml diff must not report them as drift. This happens
// after a failed first deploy: the ids are written back, but the refresh
// that fills server defaults never runs (FCR 716). A server value of
// false stays, so a real change still drifts.
func alignOmittedPortDefaults(server, local *PulledApp) {
	if server == nil || local == nil {
		return
	}
	omitted := make(map[int]bool, len(local.ServicePortMappings))
	for _, p := range local.ServicePortMappings {
		if p.StandardHttps == nil {
			omitted[p.Port] = true
		}
	}
	for i := range server.ServicePortMappings {
		sp := &server.ServicePortMappings[i]
		if omitted[sp.Port] && sp.StandardHTTPSValue() {
			sp.StandardHttps = nil
		}
	}
}
