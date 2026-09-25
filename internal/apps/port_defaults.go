package apps

import "strings"

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

// isStandardHttpsPath reports whether a server-only path summary names a
// port's standardHttps, e.g. "servicePortMappings[0].standardHttps (false)".
// Conductor reads an omitted standardHttps as true, so omitting it resets
// the value; the deploy gate treats it like other omit-resets fields
// (FCR 732).
func isStandardHttpsPath(path string) bool {
	if !strings.HasPrefix(path, "servicePortMappings[") {
		return false
	}
	i := strings.IndexByte(path, ']')
	return i >= 0 && strings.HasPrefix(path[i+1:], ".standardHttps")
}

// StandardHttpsResetHint returns a one-line remedy when the deploy would
// reset a port's standardHttps, or "" when none of clearOnOmit does.
func StandardHttpsResetHint(clearOnOmit []string) string {
	for _, f := range clearOnOmit {
		if isStandardHttpsPath(f) {
			return "  Your yaml omits standardHttps on a port where the server has false. Omitting it turns standard HTTPS back on.\n" +
				"  To keep it off, set `standardHttps: false` on that port in your yaml."
		}
	}
	return ""
}
