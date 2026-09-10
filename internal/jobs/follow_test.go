package jobs

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestFollowReturnsCompleteTerminalResponseWithoutRefetch(t *testing.T) {
	var statusReads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/jobs/55555555-5555-4555-8555-555555555555":
			statusReads.Add(1)
			_, _ = writer.Write([]byte(`{"id":"55555555-5555-4555-8555-555555555555","status":"completed","futureField":12.5}`))
		case "/jobs/55555555-5555-4555-8555-555555555555/workitems":
			_, _ = writer.Write([]byte(`{"jobId":"55555555-5555-4555-8555-555555555555","workItems":[],"hasMore":false}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(server.Close)

	service := &Service{baseURL: server.URL, httpClient: server.Client(), token: "test-token"}
	var progress bytes.Buffer
	final, err := FollowJobWithServiceToWriterResult(
		context.Background(),
		service,
		"55555555-5555-4555-8555-555555555555",
		&progress,
	)
	if err != nil {
		t.Fatal(err)
	}
	if final == nil || !bytes.Contains(final.RawBody, []byte(`"futureField":12.5`)) {
		t.Fatalf("final response was not retained: %#v", final)
	}
	if got := statusReads.Load(); got != 1 {
		t.Fatalf("job status reads = %d, want 1", got)
	}
}
