package vpn

import (
	"encoding/json"
	"io"
)

// maxBodyBytes bounds a decoded HTTP body. The state document is a few kilobytes even for an
// account with many clusters; a cap keeps a misrouted response from filling the daemon's memory.
const maxBodyBytes = 4 * 1024 * 1024

// decodeBody unmarshals a bounded HTTP response body into out.
func decodeBody(r io.Reader, out any) error {
	return json.NewDecoder(io.LimitReader(r, maxBodyBytes)).Decode(out)
}
