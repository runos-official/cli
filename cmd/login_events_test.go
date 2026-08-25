package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

/*
RunOS Desktop runs the CLI and reads its output. A person clicking Sign In there got a spinner: no
device id to check against the browser, no URL to fall back on when the browser did not open, and
no way to tell "waiting for you" from "hung". The device id in particular is the whole
anti-spoofing property of this flow, and it was on stdout as prose, unreachable.
*/
func decodeEvents(t *testing.T, raw string) []map[string]any {
	t.Helper()
	var events []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("every line must be its own JSON object; %q: %v", line, err)
		}
		events = append(events, event)
	}
	return events
}

func TestJSONSignInEmitsOneObjectPerLine(t *testing.T) {
	// NDJSON rather than one document at the end, because the point is a status that CHANGES while
	// the person is looking at it. A single document could only be written once the flow finished,
	// by which time nobody needs it.
	var out bytes.Buffer
	report := &jsonSignIn{out: &out}
	report.DeviceCode("a1b2c3", "https://console.example/account/connect-device/a1b2c3-tok", true)
	report.Pending()
	report.Pending()
	report.Authorized()

	events := decodeEvents(t, out.String())
	if len(events) != 4 {
		t.Fatalf("expected 4 events, got %d: %q", len(events), out.String())
	}
	if events[0]["event"] != "device_code" {
		t.Fatalf("the device code must come first, got %v", events[0]["event"])
	}
	if events[0]["deviceId"] != "a1b2c3" {
		t.Fatalf("the device id is what the person checks against the browser; got %v", events[0]["deviceId"])
	}
	if events[0]["url"] == "" || events[0]["url"] == nil {
		t.Fatal("the URL must be present: it is the only way in when the browser does not open")
	}
	if events[3]["event"] != "authorized" {
		t.Fatalf("expected authorized last, got %v", events[3]["event"])
	}
}

func TestJSONSignInReportsBrowserOpenedEitherWay(t *testing.T) {
	// A browser that did not open is NOT a failure: it is the case the URL exists for, and a caller
	// should show it prominently rather than as a footnote. So it must be stated, not inferred from
	// the absence of a field.
	for _, opened := range []bool{true, false} {
		var out bytes.Buffer
		(&jsonSignIn{out: &out}).DeviceCode("id", "https://example", opened)
		events := decodeEvents(t, out.String())
		got, ok := events[0]["browserOpened"].(bool)
		if !ok || got != opened {
			t.Fatalf("browserOpened must be stated as %v; got %v", opened, events[0]["browserOpened"])
		}
	}
}

func TestJSONSignInReportsAFailureWithAReasonAndASentence(t *testing.T) {
	// The reason is for the caller to branch on; the message is for the person. A UI that had only
	// the sentence would be matching on prose that is expected to be reworded.
	var out bytes.Buffer
	(&jsonSignIn{out: &out}).Failed("expired", "authorization expired - please try again")
	events := decodeEvents(t, out.String())
	if events[0]["event"] != "error" || events[0]["reason"] != "expired" {
		t.Fatalf("expected an error event carrying the reason, got %v", events[0])
	}
	if events[0]["message"] == "" {
		t.Fatal("a failure must also carry the sentence for a person")
	}
}

func TestTextSignInStillReadsAsItAlwaysDid(t *testing.T) {
	// The terminal output is unchanged by the refactor. It names the device id and tells the person
	// to verify it, which is the instruction that makes the check happen at all.
	var out bytes.Buffer
	report := textSignIn{out: &out}
	report.DeviceCode("a1b2c3", "https://console.example/x", true)
	report.Pending()
	got := out.String()
	if !strings.Contains(got, "Device ID: a1b2c3 - verify this matches the browser") {
		t.Fatalf("the verify instruction must survive: %q", got)
	}
	if !strings.Contains(got, "https://console.example/x") {
		t.Fatalf("the URL must survive: %q", got)
	}
}
