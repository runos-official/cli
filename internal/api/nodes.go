package api

import (
	"fmt"
	"net/http"
	"net/url"
)

// A node record carries an operator assigned display NAME beside the node
// id the operator types on the command line. The name is a proto3 string,
// so a node with no assigned name returns the empty string, never null and
// never absent. A caller therefore tests the name for the empty string.

// NodeName reads the display name of one node.
//
// GET /:aid/:cid/nodes/:nid, the same read the `nodes show` command makes.
// Any result that is not 2xx is an error here: an unknown node id answers
// 404, and a node id that fails conductor's shape guard answers 400, and
// neither result carries a name to show.
func (c *Client) NodeName(accountID, clusterID, nodeID, token string) (string, error) {
	if accountID == "" {
		return "", fmt.Errorf("account ID not set")
	}
	if clusterID == "" {
		return "", fmt.Errorf("cluster ID not set")
	}
	if nodeID == "" {
		return "", fmt.Errorf("node ID not set")
	}
	path := "/" + url.PathEscape(accountID) +
		"/" + url.PathEscape(clusterID) +
		"/nodes/" + url.PathEscape(nodeID)
	result, err := c.Do(http.MethodGet, path, token, nil)
	if err != nil {
		return "", err
	}
	if !result.OK() {
		if msg := result.ErrorMessage(); msg != "" {
			return "", fmt.Errorf("%s (HTTP %d)", msg, result.StatusCode)
		}
		return "", fmt.Errorf("could not read the node (HTTP %d)", result.StatusCode)
	}
	var node struct {
		Name string `json:"name"`
	}
	if err := result.Decode(&node); err != nil {
		return "", err
	}
	return node.Name, nil
}
