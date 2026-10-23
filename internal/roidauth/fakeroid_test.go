package roidauth

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeRoid speaks the roid wire protocol the CLI uses: the device flow,
// refresh rotation with reuse detection, logout and one protected route.
// It verifies every proof the way roid does, so a proof the CLI gets wrong
// fails here before it reaches a real server.
type fakeRoid struct {
	t   *testing.T
	srv *httptest.Server

	mu         sync.Mutex
	jkt        string // the thumbprint the device flow was bound to
	deviceCode string
	userCode   string
	approved   bool
	pendingFor int // polls answered authorization_pending before approval counts
	polls      int
	refresh    map[string]string // refresh token -> family
	spent      map[string]string // spent refresh token -> family
	access     map[string]string // access token -> family
	revoked    map[string]bool   // family -> revoked
	seenProof  map[string]bool
	person     string
	accounts   []string
	refreshes  int
	logouts    int
	accessTTL  int
	api        *httptest.Server // set by splitAPIHost: the host that serves the identity routes
}

func newFakeRoid(t *testing.T) *fakeRoid {
	f := &fakeRoid{t: t, refresh: map[string]string{}, spent: map[string]string{}, access: map[string]string{},
		revoked: map[string]bool{}, seenProof: map[string]bool{}, person: "p_alice", accounts: []string{"acct1"}, accessTTL: 600}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /device/authorize", f.authorize)
	mux.HandleFunc("POST /device/token", f.deviceToken)
	mux.HandleFunc("POST /cli/token", f.cliToken)
	mux.HandleFunc("POST /cli/logout", f.cliLogout)
	mux.HandleFunc("GET /v1/me", f.me)
	mux.HandleFunc("GET /user/accounts", f.userAccounts)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// splitAPIHost makes the fake behave like prod, where the issuer host serves the OIDC routes only and
// the API host serves /v1/me and /user/accounts: the issuer answers 404 for those two.
func (f *fakeRoid) splitAPIHost() {
	f.api = httptest.NewServer(f.srv.Config.Handler)
	f.t.Cleanup(f.api.Close)
}

// origin is the origin a proof must name for r: the host that received it.
func (f *fakeRoid) origin(r *http.Request) string {
	if f.api != nil && r.Host == strings.TrimPrefix(f.api.URL, "http://") {
		return f.api.URL
	}
	return f.srv.URL
}

// identityHere says whether this host serves the identity routes.
func (f *fakeRoid) identityHere(r *http.Request) bool {
	return f.api == nil || f.origin(r) == f.api.URL
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func shaHex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func oauthError(w http.ResponseWriter, status int, code, desc string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "error_description": desc})
}

func writeJSONBody(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// checkProof verifies the X-RunOS-Proof header like roid's proof.Verifier:
// right key, right purpose and subject, fresh, and never seen before.
func (f *fakeRoid) checkProof(r *http.Request, purpose, subject string) bool {
	raw, err := base64.RawURLEncoding.DecodeString(r.Header.Get(ProofHeader))
	if err != nil {
		return false
	}
	var p struct {
		PublicKey []byte
		Purpose   string
		Subject   string
		Method    string
		Target    string
		IssuedAt  int64
		ID        string
		Signature []byte
	}
	if json.Unmarshal(raw, &p) != nil || len(p.PublicKey) != ed25519.PublicKeySize {
		return false
	}
	h := sha256.Sum256(p.PublicKey)
	if base64.RawURLEncoding.EncodeToString(h[:]) != f.jkt || p.Purpose != purpose || p.Subject != subject {
		return false
	}
	// FPL321 c5: the proof names this request: the method and the fake's
	// own origin with the path (roid's issuer origin, never the Host header).
	if p.Method != r.Method || p.Target != f.origin(r)+r.URL.EscapedPath() {
		return false
	}
	msg := "runos-proof-v2\x00" + p.Purpose + "\x00" + p.Subject + "\x00" + p.Method + "\x00" + p.Target +
		"\x00" + strconv.FormatInt(p.IssuedAt, 10) + "\x00" + p.ID
	if !ed25519.Verify(p.PublicKey, []byte(msg), p.Signature) {
		return false
	}
	if d := time.Since(time.Unix(p.IssuedAt, 0)); d > time.Minute || d < -time.Minute {
		return false
	}
	key := p.Target + "\x00" + p.ID // replay key (thumbprint, target, id); one key here
	if p.ID == "" || f.seenProof[key] {
		return false
	}
	f.seenProof[key] = true
	return true
}

func (f *fakeRoid) authorize(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.PostForm.Get("client_id") != ClientID || r.PostForm.Get("dpop_jkt") == "" || r.Header.Get("Authorization") != "" {
		oauthError(w, 400, "invalid_request", "bad device authorization request")
		return
	}
	f.jkt = r.PostForm.Get("dpop_jkt")
	f.deviceCode, f.userCode = "dc_"+randHex(16), "WDJB-MJHT"
	writeJSONBody(w, map[string]any{"device_code": f.deviceCode, "user_code": f.userCode,
		"verification_uri": f.srv.URL + "/device", "verification_uri_complete": f.srv.URL + "/device?user_code=" + f.userCode,
		"expires_in": 600, "interval": 5})
}

func (f *fakeRoid) issueLocked(family string) map[string]any {
	at, rt := "at_"+randHex(16), "rt_"+randHex(16)
	f.access[at], f.refresh[rt] = family, family
	return map[string]any{"access_token": at, "refresh_token": rt, "token_type": "DPoP", "expires_in": f.accessTTL, "credential_id": family}
}

func (f *fakeRoid) deviceToken(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.PostForm.Get("grant_type") != "urn:ietf:params:oauth:grant-type:device_code" || r.PostForm.Get("device_code") != f.deviceCode {
		oauthError(w, 400, "invalid_grant", "unknown device code")
		return
	}
	if !f.checkProof(r, "device-poll", shaHex(f.deviceCode)) {
		oauthError(w, 400, "invalid_dpop_proof", "bad proof")
		return
	}
	f.polls++
	if !f.approved || f.polls <= f.pendingFor {
		oauthError(w, 400, "authorization_pending", "")
		return
	}
	f.deviceCode = "" // one use
	writeJSONBody(w, f.issueLocked("fam_"+randHex(4)))
}

func (f *fakeRoid) cliToken(w http.ResponseWriter, r *http.Request) {
	var in struct {
		GrantType    string `json:"grant_type"`
		RefreshToken string `json:"refresh_token"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.GrantType != "refresh_token" {
		oauthError(w, 400, "unsupported_grant_type", "")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if fam, reused := f.spent[in.RefreshToken]; reused {
		f.revoked[fam] = true
		oauthError(w, 400, "invalid_grant", "refresh token reuse: family revoked")
		return
	}
	fam, ok := f.refresh[in.RefreshToken]
	if !ok || f.revoked[fam] {
		oauthError(w, 400, "invalid_grant", "unknown refresh token")
		return
	}
	if !f.checkProof(r, "refresh", shaHex(in.RefreshToken)) {
		oauthError(w, 400, "invalid_dpop_proof", "bad proof")
		return
	}
	delete(f.refresh, in.RefreshToken)
	f.spent[in.RefreshToken] = fam
	f.refreshes++
	writeJSONBody(w, f.issueLocked(fam))
}

func (f *fakeRoid) cliLogout(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token string `json:"token"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logouts++
	if fam, ok := f.refresh[in.Token]; ok {
		f.revoked[fam] = true
	}
	writeJSONBody(w, map[string]string{"status": "revoked"})
}

// accessFamily checks a bearer access token and its access proof.
func (f *fakeRoid) accessFamily(r *http.Request) (string, bool) {
	at := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	fam, ok := f.access[at]
	if !ok || f.revoked[fam] || !f.checkProof(r, "access", shaHex(at)) {
		return "", false
	}
	return fam, true
}

func (f *fakeRoid) me(w http.ResponseWriter, r *http.Request) {
	if !f.identityHere(r) {
		http.NotFound(w, r)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.accessFamily(r); !ok {
		oauthError(w, 401, "invalid_token", "")
		return
	}
	writeJSONBody(w, map[string]any{"person_id": f.person, "credential_class": "cli", "assurance": 1, "method": "password"})
}

func (f *fakeRoid) userAccounts(w http.ResponseWriter, r *http.Request) {
	if !f.identityHere(r) {
		http.NotFound(w, r)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.accessFamily(r); !ok {
		oauthError(w, 401, "invalid_token", "")
		return
	}
	writeJSONBody(w, map[string]any{"accounts": f.accounts})
}

func (f *fakeRoid) approve() {
	f.mu.Lock()
	f.approved = true
	f.mu.Unlock()
}
