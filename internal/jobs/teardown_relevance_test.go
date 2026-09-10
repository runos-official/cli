package jobs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestFollowIgnoresEmptyMetadataOnUnrelatedJobs(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"app.deploy", "app.run", "apps.build", "apps.sync", "services.sync", "harbor.build", "cloudProviders.addServer"} {
		t.Run(kind, func(t *testing.T) {
			body := fmt.Sprintf(`{"id":"job","type":%q,"status":"completed","teardowns":[],"teardownRead":{"aid":"a","cid":"c","jobId":"job"},"teardownNextCursor":null,"teardownReadError":null}`, kind)
			var job JobStatus
			if err := json.Unmarshal([]byte(body), &job); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			EmitFollowDeltas(&out, &job, nil, NewFollowState())
			if got := out.String(); got != "job job: completed\n" {
				t.Fatalf("unrelated follow changed: %s", got)
			}
		})
	}
}

func TestFollowRetainsTrackingBeforeTeardownEnumeration(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"cluster.reset", "nodes.deleteAndReset", "cloudProviders.removeServer"} {
		t.Run(kind, func(t *testing.T) {
			body := fmt.Sprintf(`{"id":"job","type":%q,"status":"completed","teardowns":[],"teardownRead":{"aid":"a","cid":"c","jobId":"job"}}`, kind)
			var job JobStatus
			if err := json.Unmarshal([]byte(body), &job); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			EmitFollowDeltas(&out, &job, nil, NewFollowState())
			for _, want := range []string{"bookkeeping completed", "No teardown records are available yet.", "--job-id job"} {
				if !strings.Contains(out.String(), want) {
					t.Errorf("missing %q:\n%s", want, out.String())
				}
			}
		})
	}
}
