package services

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestSyncPlanJSONKeepsEmptyCreateDistinctFromUpdate(t *testing.T) {
	for _, tc := range []struct {
		name string
		plan SyncPlan
		want map[string]any
	}{
		{
			name: "empty create",
			plan: SyncPlan{Type: "valkey", CID: "cluster1", CreateBody: map[string]any{}},
			want: map[string]any{"type": "valkey", "cid": "cluster1", "createBody": map[string]any{}},
		},
		{
			name: "populated create",
			plan: SyncPlan{Type: "valkey", CID: "cluster1", CreateBody: map[string]any{"name": "test-service"}},
			want: map[string]any{"type": "valkey", "cid": "cluster1", "createBody": map[string]any{"name": "test-service"}},
		},
		{
			name: "update",
			plan: SyncPlan{Type: "valkey", ID: "abc12", CID: "cluster1", PatchBody: map[string]any{"replicas": 2}},
			want: map[string]any{"type": "valkey", "id": "abc12", "cid": "cluster1", "patchBody": map[string]any{"replicas": float64(2)}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(&tc.plan)
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("JSON plan = %s, want %#v", raw, tc.want)
			}
		})
	}
}
