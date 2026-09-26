package deploy

// FoldPortShorthand rewrites the legacy top-level `port:` / `standardHttps:`
// pair into servicePortMappings, the form the server stores and apps pull
// writes (FCR 753). The rule matches the conductor's deploy intake: an
// existing mappings list wins and the shorthand is dropped; otherwise a
// scalar port becomes one mapping whose standardHttps defaults to true and
// is spelled out, as a pulled yaml spells it. Call it on the writeback
// after the prepare request, so the request body is unchanged.
func FoldPortShorthand(c *DeployConfig) {
	if c == nil {
		return
	}
	if len(c.ServicePortMappings) == 0 && c.Port > 0 {
		https := true
		if c.StandardHttps != nil {
			https = *c.StandardHttps
		}
		c.ServicePortMappings = []ServicePortMapping{{Port: c.Port, StandardHttps: &https}}
	}
	if len(c.ServicePortMappings) > 0 {
		c.Port = 0
		c.StandardHttps = nil
	}
}
