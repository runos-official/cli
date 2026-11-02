package roidauth

import (
	"encoding/json"
	"testing"
)

func TestAccountIDReadsObjectsAndBareIds(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"conductor object": {`{"aid":"acct1","name":"x","role":"admin"}`, "acct1"},
		"roid bare id":     {`"acct2"`, "acct2"},
		"unknown shape":    {`42`, ""},
		"object no aid":    {`{"name":"x"}`, ""},
	} {
		if got := accountID(json.RawMessage(tc.in)); got != tc.want {
			t.Errorf("%s: got %q want %q", name, got, tc.want)
		}
	}
}
