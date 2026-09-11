package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func evictionPayload() map[string]any {
	return map[string]any{
		"outcome": "matched", "hostname": "host-new", "nid": nil,
		"runosNodeName": " worker one ", "runosNid": "node-full-0123456789",
		"source": "nodes", "candidateNids": []string{}, "unavailableSources": []string{},
	}
}

func TestReadEvictionTargetOutcomes(t *testing.T) {
	cases := []struct {
		name, outcome string
		changes       map[string]any
		nodeInput     bool
	}{
		{"node only", "matched", nil, false},
		{"device and current", "matched", map[string]any{"nid": "node-full-0123456789", "source": "devices"}, false},
		{"device remnant", "no_record", map[string]any{"nid": "node-full-0123456789", "source": "devices", "runosNid": "", "runosNodeName": ""}, false},
		{"complete absence", "no_record", map[string]any{"source": nil, "runosNid": "", "runosNodeName": ""}, false},
		{"unknown with cleanup", "unknown", map[string]any{"nid": "node-full-0123456789", "source": "devices", "runosNid": "", "runosNodeName": "", "unavailableSources": []string{"nodes"}}, false},
		{"unknown without facts", "unknown", map[string]any{"source": nil, "runosNid": "", "runosNodeName": "", "unavailableSources": []string{"devices", "nodes"}}, false},
		{"unknown retains current facts", "unknown", map[string]any{"unavailableSources": []string{"devices"}}, false},
		{"ambiguous", "ambiguous", map[string]any{"source": nil, "runosNid": "", "runosNodeName": "", "candidateNids": []string{"node-full-0123456789", "node-full-9876543210"}}, false},
		{"missing hostname with identity", "missing_hostname", map[string]any{"hostname": nil}, true},
		{"missing hostname without identity", "missing_hostname", map[string]any{"hostname": nil, "source": nil, "runosNid": "", "runosNodeName": ""}, true},
		{"raw blank name", "matched", map[string]any{"runosNodeName": ""}, false},
		{"raw control name", "matched", map[string]any{"runosNodeName": "bad\nname"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := evictionPayload()
			payload["outcome"] = tc.outcome
			for key, value := range tc.changes {
				payload[key] = value
			}
			body, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			requests := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer test-token" {
					t.Errorf("unexpected request: %s, auth=%q", r.Method, r.Header.Get("Authorization"))
				}
				fmt.Fprint(w, string(body))
			}))
			defer srv.Close()
			nid, hostname := "", "host-new"
			if tc.nodeInput {
				nid, hostname = "node-full-0123456789", ""
			}
			got, err := NewClient(srv.URL).ReadEvictionTarget("account", "cluster", nid, hostname, "test-token")
			if err != nil {
				t.Fatal(err)
			}
			var want EvictionTarget
			if err := json.Unmarshal(body, &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(*got, want) || requests != 1 {
				t.Fatalf("target=%+v, want=%+v; requests=%d", got, want, requests)
			}
		})
	}
}

func TestReadEvictionTargetEscapesInputs(t *testing.T) {
	for _, field := range []string{"nid", "hostname"} {
		t.Run(field, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.EscapedPath() != "/account%2Fone/cluster%2Fone/storage-groups/evict-node-target" {
					t.Errorf("path=%s", r.URL.EscapedPath())
				}
				if len(r.URL.Query()) != 1 || r.URL.Query().Get(field) != "target /&?+" {
					t.Errorf("query=%v", r.URL.Query())
				}
				payload := evictionPayload()
				payload["hostname"] = "target /&?+"
				json.NewEncoder(w).Encode(payload)
			}))
			defer srv.Close()
			nid, host := "", "target /&?+"
			if field == "nid" {
				nid, host = host, ""
			}
			if _, err := NewClient(srv.URL).ReadEvictionTarget("account/one", "cluster/one", nid, host, "token"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestEvictionTargetRejectsMalformedFacts(t *testing.T) {
	for _, key := range []string{"outcome", "hostname", "nid", "runosNodeName", "runosNid", "source", "candidateNids", "unavailableSources"} {
		t.Run("missing "+key, func(t *testing.T) {
			payload := evictionPayload()
			delete(payload, key)
			body, _ := json.Marshal(payload)
			if _, err := decodeEvictionTarget(body); err == nil {
				t.Fatal("accepted missing field")
			}
		})
	}
	cases := []struct {
		name  string
		patch map[string]any
	}{
		{"unknown outcome", map[string]any{"outcome": "future"}},
		{"null name", map[string]any{"runosNodeName": nil}},
		{"null id", map[string]any{"runosNid": nil}},
		{"numeric id", map[string]any{"runosNid": 42}},
		{"null candidates", map[string]any{"candidateNids": nil}},
		{"null unavailable", map[string]any{"unavailableSources": nil}},
		{"bad source", map[string]any{"source": "future"}},
		{"null source matched", map[string]any{"source": nil}},
		{"missing matched id", map[string]any{"runosNid": "", "runosNodeName": ""}},
		{"blank cleanup id", map[string]any{"nid": " ", "source": "devices"}},
		{"invented cleanup", map[string]any{"nid": "node-full-0123456789"}},
		{"devices without cleanup", map[string]any{"source": "devices"}},
		{"conflicting identity", map[string]any{"nid": "different-id", "source": "devices"}},
		{"absence with identity", map[string]any{"outcome": "no_record"}},
		{"absence with failed source", map[string]any{"outcome": "no_record", "runosNid": "", "runosNodeName": "", "source": nil, "unavailableSources": []string{"nodes"}}},
		{"unknown without failure", map[string]any{"outcome": "unknown"}},
		{"failed nodes with identity", map[string]any{"outcome": "unknown", "unavailableSources": []string{"nodes"}}},
		{"bad failure source", map[string]any{"outcome": "unknown", "unavailableSources": []string{"future"}}},
		{"control id", map[string]any{"runosNid": "node\x1b[2J"}},
		{"control hostname", map[string]any{"hostname": "host\nspoof"}},
		{"ambiguous single candidate", map[string]any{"outcome": "ambiguous", "source": nil, "runosNid": "", "runosNodeName": "", "candidateNids": []string{"node-one"}}},
		{"ambiguous duplicate candidates", map[string]any{"outcome": "ambiguous", "source": nil, "runosNid": "", "runosNodeName": "", "candidateNids": []string{"node-one", "node-one"}}},
		{"missing hostname carries host", map[string]any{"outcome": "missing_hostname"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := evictionPayload()
			for key, value := range tc.patch {
				payload[key] = value
			}
			body, _ := json.Marshal(payload)
			if _, err := decodeEvictionTarget(body); err == nil {
				t.Fatal("accepted inconsistent response")
			}
		})
	}
}

func TestReadEvictionTargetFailures(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"http failure", `{"error":"unavailable"}`, 503},
		{"auth failure", `{"error":"unauthorized"}`, 401},
		{"empty", "", 200},
		{"html", "<html>error</html>", 200},
		{"array", "[]", 200},
		{"null", "null", 200},
		{"mismatched target", `{"outcome":"no_record","hostname":"different","nid":null,"runosNodeName":"","runosNid":"","source":null,"candidateNids":[],"unavailableSources":[]}`, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			if _, err := NewClient(srv.URL).ReadEvictionTarget("account", "cluster", "", "host-new", "token"); err == nil {
				t.Fatal("expected lookup failure")
			}
		})
	}
	t.Run("timeout", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
		defer srv.Close()
		_, err := NewClientWithTimeout(srv.URL, 20*time.Millisecond).ReadEvictionTarget("account", "cluster", "", "host-new", "token")
		if err == nil {
			t.Fatal("expected timeout")
		}
	})
	t.Run("invalid input makes no call", func(t *testing.T) {
		requests := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++ }))
		defer srv.Close()
		for _, args := range [][4]string{{"", "cluster", "", "host"}, {"account", "", "", "host"}, {"account", "cluster", "", ""}, {"account", "cluster", "node", "host"}} {
			_, err := NewClient(srv.URL).ReadEvictionTarget(args[0], args[1], args[2], args[3], "token")
			if err == nil || strings.Contains(err.Error(), "http") {
				t.Fatalf("expected local input error, got %v", err)
			}
		}
		if requests != 0 {
			t.Fatalf("invalid input sent %d requests", requests)
		}
	})
}
