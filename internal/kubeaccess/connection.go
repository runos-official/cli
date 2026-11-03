package kubeaccess

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"

	"github.com/runos-official/cli/internal/api"
)

// Connection is the public half of a kubeconfig cluster entry: where the API
// server is and which certificate authority signs it. It holds no credential.
// TLSServerName is set only when Server is a node address.
type Connection struct {
	Server        string
	CAPEM         []byte
	TLSServerName string
}

// ConnectionFetcher reads a cluster's Connection from the conductor route
// GET /{aid}/{cid}/kube-connection, with the credential the CLI already holds.
// Any member of the account may read it. It never touches the admin kubeconfig.
type ConnectionFetcher struct {
	BaseURL string
	Account string
	Cluster string
	Token   func() (string, error)
}

// ConnectionError is a conductor answer that is not a connection.
type ConnectionError struct {
	Status int
	Code   string
}

func (e *ConnectionError) Error() string {
	return fmt.Sprintf("the RunOS API refused the cluster connection request (%d %s)", e.Status, e.Code)
}

// Fetch makes one call. A refusal is a *ConnectionError, a transport failure a
// *TransportError, and a session problem is the error Token returned.
func (f *ConnectionFetcher) Fetch() (Connection, error) {
	tok, err := f.Token()
	if err != nil {
		return Connection{}, err
	}
	path := "/" + url.PathEscape(f.Account) + "/" + url.PathEscape(f.Cluster) + "/kube-connection"
	res, err := api.NewClient(f.BaseURL).Do("GET", path, tok, nil)
	if err != nil {
		return Connection{}, &TransportError{Err: err}
	}
	if !res.OK() {
		var b struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(res.Body, &b)
		return Connection{}, &ConnectionError{Status: res.StatusCode, Code: b.Code}
	}
	var body struct {
		Server        string `json:"server"`
		CAPEM         string `json:"ca_pem"`
		TLSServerName string `json:"tls_server_name"`
	}
	if err := res.Decode(&body); err != nil {
		return Connection{}, err
	}
	if body.Server == "" || body.CAPEM == "" {
		return Connection{}, errors.New("the RunOS API answered without the cluster's API server address and CA")
	}
	return Connection{Server: body.Server, CAPEM: []byte(body.CAPEM), TLSServerName: body.TLSServerName}, nil
}

const passFlags = "Or pass --server and --ca-file."

// ConnectionMessage turns an error from Fetch into one short line that says
// what is wrong and what to do.
func ConnectionMessage(err error) string {
	var ce *ConnectionError
	var te *TransportError
	switch {
	case errors.As(err, &ce):
		switch {
		case ce.Code == "cluster.api_endpoint_not_configured":
			return "RunOS does not know this cluster's API server address or CA yet. Ask an account admin, or pass --server and --ca-file."
		case ce.Status == 401:
			return "Your RunOS sign-in is no longer valid. Run 'runos login', then try again."
		case ce.Status == 403:
			return "Your role in this account may not read this cluster's connection details. Ask an account admin. " + passFlags
		case ce.Status == 404:
			return "The account does not hold this cluster. Check --cluster and --account."
		}
		return fmt.Sprintf("RunOS could not return the cluster's connection details (%d). %s", ce.Status, passFlags)
	case errors.As(err, &te):
		return "Could not reach RunOS to read the cluster's connection details: " + oneLine(err.Error()) + ". " + passFlags
	}
	return "Could not read the cluster's connection details: " + oneLine(err.Error())
}
