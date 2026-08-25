package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

/*
How the browser device-code flow reports itself.

It used to write sentences straight to an io.Writer, which is right for a terminal and useless to
anything else. RunOS Desktop runs the CLI and reads its output, so a person clicking Sign In there
got a spinner: no device id to check against the browser, no URL to fall back on when the browser
did not open, and no way to tell "waiting for you" from "hung". The two facts a person needs to
complete this safely were on stdout and unreachable.

ONE FLOW, TWO REPORTERS. The state transitions live in browserAuthenticate and are not duplicated;
a second code path for authentication is exactly the thing that drifts and then differs in the one
case nobody tested.
*/
type signInReporter interface {
	// DeviceCode fires once, as soon as the device id and URL exist. The id is what the person
	// compares against the browser, which is the whole anti-spoofing property of this flow.
	DeviceCode(deviceID, browserURL string, browserOpened bool)
	// Pending fires on each poll that finds the authorization still outstanding.
	Pending()
	// Authorized fires once the browser has authorised, before the token exchange.
	Authorized()
	// Failed fires once with a machine-readable reason and the sentence for a person.
	Failed(reason, message string)
}

// textSignIn writes the flow as prose, for a terminal. This is the output `runos login` has always
// produced and it is unchanged.
type textSignIn struct{ out io.Writer }

func (t textSignIn) DeviceCode(deviceID, browserURL string, browserOpened bool) {
	fmt.Fprintf(t.out, "Opening browser to authenticate...\n")
	fmt.Fprintf(t.out, "Device ID: %s - verify this matches the browser\n", deviceID)
	if browserOpened {
		fmt.Fprintf(t.out, "If the browser doesn't open, visit: %s\n\n", browserURL)
	} else {
		fmt.Fprintf(t.out, "\nCouldn't open browser automatically (this is normal on remote servers).\n")
		fmt.Fprintf(t.out, "Please open this URL in your browser:\n\n  %s\n\n", browserURL)
	}
	fmt.Fprintf(t.out, "Waiting for authorization")
}

func (t textSignIn) Pending()           { fmt.Fprint(t.out, ".") }
func (t textSignIn) Authorized()        { fmt.Fprintf(t.out, "\n\nExchanging token...") }
func (t textSignIn) Failed(_, _ string) { fmt.Fprintln(t.out) }

/*
jsonSignIn writes one JSON object per line, flushed as it happens.

NDJSON rather than one document at the end, because the point is a status that CHANGES while the
person is looking at it. A single document could only be written once the flow had finished, by
which time nobody needs it.
*/
type jsonSignIn struct {
	out io.Writer
	mu  sync.Mutex
}

type signInEvent struct {
	Event    string `json:"event"`
	DeviceID string `json:"deviceId,omitempty"`
	URL      string `json:"url,omitempty"`
	// BrowserOpened says whether the CLI managed to open a browser. False is not a failure: it is
	// the case the URL exists for, and the caller should show it prominently rather than as a
	// footnote.
	BrowserOpened *bool  `json:"browserOpened,omitempty"`
	Reason        string `json:"reason,omitempty"`
	Message       string `json:"message,omitempty"`
}

func (j *jsonSignIn) emit(event signInEvent) {
	j.mu.Lock()
	defer j.mu.Unlock()
	encoded, err := json.Marshal(event)
	if err != nil {
		return
	}
	fmt.Fprintf(j.out, "%s\n", encoded)
	if flusher, ok := j.out.(interface{ Flush() error }); ok {
		_ = flusher.Flush()
	}
}

func (j *jsonSignIn) DeviceCode(deviceID, browserURL string, browserOpened bool) {
	opened := browserOpened
	j.emit(signInEvent{Event: "device_code", DeviceID: deviceID, URL: browserURL, BrowserOpened: &opened})
}

func (j *jsonSignIn) Pending()    { j.emit(signInEvent{Event: "pending"}) }
func (j *jsonSignIn) Authorized() { j.emit(signInEvent{Event: "authorized"}) }
func (j *jsonSignIn) Failed(reason, message string) {
	j.emit(signInEvent{Event: "error", Reason: reason, Message: message})
}
