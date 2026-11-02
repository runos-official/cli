package roidapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"

	"github.com/runos-official/cli/internal/api"
	"github.com/runos-official/cli/internal/config"
)

// Router sends the requests for routes that only rapi serves (the access API and the Kubernetes token
// route) to the right one of RunOS's two hosts without asking the person which.
//
// The two hosts differ by environment. On PROD rapi fronts the API host, and roid's own sign-in host
// answers those routes with 404 not_on_this_host. On DEV the API host goes to conductor, which answers
// 404 for them, and roid's own host serves every route because no front service is named. So a rapi-only
// route is sent to the gateway (the API URL) first. A 404 means "not served here", and the same request
// is then sent once to the issuer. Any other answer, including 401, 403, 409 and 5xx, is final and is
// never retried: those come from a host that knows the route.
//
// Security. Both addresses come from the CLI's configuration (config.AuthIssuer, and the API URL that
// RUNOS_API_URL may replace) when the Router is built. Nothing in an answer (status, body, header,
// redirect) can name a host, so a 404 from an unexpected server cannot make the CLI send credentials to a
// host that is neither the configured API URL nor the configured issuer. Both hosts are RunOS's own and the
// bearer is already sent to either: the session lists both in its proof hosts. The retry is a new HTTP
// request. The proof transport adds a proof to each request for the URL it goes to, so the retry carries a
// proof that names the second origin (RFC 9449 htu), and the proof sent to the first host is never reused.
// A 404 means the route did nothing on that host, so resending a write is safe.
type Router struct {
	gateway, issuer string
	gw, is          *api.Client
}

// NewRouter builds a Router for the API URL (gateway) and the sign-in issuer. Both are trimmed of a
// trailing slash. An empty gateway means one host, the issuer.
func NewRouter(gateway, issuer string) *Router {
	gateway, issuer = strings.TrimRight(gateway, "/"), strings.TrimRight(issuer, "/")
	if gateway == "" {
		gateway = issuer
	}
	r := &Router{gateway: gateway, issuer: issuer, gw: api.NewClient(gateway)}
	r.is = r.gw
	if !r.single() {
		r.is = api.NewClient(issuer)
	}
	return r
}

// RouterFor builds the Router for the environment in cfg: the gateway is the API URL (RUNOS_API_URL
// overrides it) and the issuer is the sign-in host. It refuses an environment that has no identity service.
func RouterFor(cfg *config.Config) (*Router, error) {
	gateway, err := GatewayURL(cfg)
	if err != nil {
		return nil, err
	}
	issuer, _ := BaseURL(cfg)
	return NewRouter(gateway, issuer), nil
}

// single reports whether the two addresses are the same host, so there is nowhere to retry.
func (r *Router) single() bool {
	return strings.EqualFold(r.gateway, r.issuer)
}

// preferred remembers, for the life of the process, which host answered a rapi route last, keyed by the
// pair of configured addresses. Two Routers built for the same pair in one run share it.
var preferred sync.Map // key string -> bool (true: the issuer is tried first)

func (r *Router) key() string { return strings.ToLower(r.gateway) + "\x00" + strings.ToLower(r.issuer) }

func (r *Router) issuerFirst() bool {
	v, _ := preferred.Load(r.key())
	b, _ := v.(bool)
	return b
}

// Send sends req to the preferred host and, on a 404 only, once to the other. Transport errors are not
// retried. A host is remembered as the one that works when it gives an answer that is not a 404 and not a
// server failure.
func (r *Router) Send(req api.Request) (*api.Result, error) {
	if r.single() {
		return r.gw.Send(req)
	}
	first, second, firstIsIssuer := r.gw, r.is, false
	if r.issuerFirst() {
		first, second, firstIsIssuer = r.is, r.gw, true
	}
	res, err := first.Send(req)
	if err != nil || res.StatusCode != http.StatusNotFound {
		if err == nil && res.StatusCode < 500 {
			preferred.Store(r.key(), firstIsIssuer)
		}
		return res, err
	}
	res2, err2 := second.Send(req)
	if err2 != nil {
		return res, nil // the second host is unreachable: the first host's 404 is the answer we have
	}
	if res2.StatusCode == http.StatusNotFound {
		// Neither host serves it (a genuine "no such role"). Keep the answer of the host that knows the
		// route: a not_on_this_host answer only says "wrong host".
		if isNotOnThisHost(res2) {
			return res, nil
		}
		return res2, nil
	}
	if res2.StatusCode < 500 {
		preferred.Store(r.key(), !firstIsIssuer)
	}
	return res2, nil
}

// isNotOnThisHost reports roid's answer for a route it serves only behind the other host.
func isNotOnThisHost(res *api.Result) bool {
	var b struct {
		Error struct {
			Reason string `json:"reason"`
		} `json:"error"`
	}
	return json.Unmarshal(res.Body, &b) == nil && b.Error.Reason == "not_on_this_host"
}
