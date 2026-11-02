# Which host answers a rapi-only roid route (FPL365, FCR 1797)

Some identity-service routes are reachable only through rapi: the access API
(`runos access roles|groups|grants|explain|actions`) and the Kubernetes token route
`POST /v1/accounts/{acct}/clusters/{cluster}/k8s/token`. Where rapi sits differs by environment:

| | API host (`GetAPIURL`, the gateway) | Sign-in host (`AuthIssuer`) |
|---|---|---|
| PROD | rapi: serves the routes | roid: `404 not_on_this_host` |
| DEV | conductor: `404` | roid: serves every route (no front service is named) |

One binary has to work on both with no `RUNOS_API_URL`.

## Design

`roidapi.Router` (`internal/roidapi/router.go`) carries every request for such a route:

1. Send to the preferred host. At the start of a run that is the gateway (the API URL).
2. If the answer is HTTP 404, and the issuer is a different address, send the same request once to
   the issuer. It is a new HTTP request, so the proof transport adds a proof that names the
   issuer origin (the proof's `Target` is the request URL, RFC 9449 `htu`); the proof sent to the
   gateway is never reused.
3. Every other status is final: 401, 403, 409, 412, 428, 429 and 5xx are never retried, and a
   connection error is not retried either.
4. The host that gave an answer which is not a 404 and not a 5xx is remembered for the rest of the
   process (keyed by the pair of configured addresses), so a later call in the same run goes to the
   right host first. On DEV the first access call costs one extra request and the rest none.
5. If both hosts answer 404 (a genuine "no such role"), the person sees the answer of the host that
   knows the route: the issuer's `not_on_this_host` is never preferred over the gateway's own 404.

`runos access ...` (`cmd/access.go: accessClient`) and `runos kube credential`
(`cmd/kube_credential.go: credentialFetcher`) both build their client from `roidapi.RouterFor(cfg)`.
Sign-in, refresh and logout are not rapi routes and still go to the issuer only.
`RUNOS_API_URL` still names the gateway (the first host). When it is the issuer address there is one
host and nothing is retried. A 404 from an overridden gateway now also tries the configured
issuer once; success paths are unchanged.

A write is safe to resend after a 404: a 404 means the route did nothing on that host. The body,
`If-Match` and step-up header are sent again unchanged.

## Why this sends nothing new

- Both hosts come only from the configuration (`config.AuthIssuer`, and `RUNOS_API_URL` or
  `ConductorURL`) and are fixed when the Router is built. Nothing in an answer (status, body,
  header, redirect) can name a host, so a 404 from an unexpected server cannot make the CLI send
  credentials anywhere except the configured API URL and the configured issuer.
- Both are RunOS's own hosts. The session already lists both as proof hosts
  (`auth.RoidSession`: `ProofHosts`), and the bearer token and its proof are bound to each host's
  origin. A proof made for one origin is refused at the other, so the retry must, and does, carry
  its own proof.
- A host that is in neither the config nor the session's proof hosts gets a bearer but no proof; the
  Router never builds such a request.

## Not covered

- `/v1/me` and `/user/accounts` (read at sign-in by `roidauth.identity`) have the same host
  sensitivity; they are not changed here.
- The step-up poll `GET /cli/stepup/{id}` rides the same Router, so it works wherever it is served.
- Nothing here was run against a live rapi or PROD; the evidence is fakes that model both host
  shapes, with the fake roid verifying the proof (method, origin and path) on every use.
