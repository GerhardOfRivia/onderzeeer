package webui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/GerhardOfRivia/onderzeeer/internal/queue"
)

func TestJobsAPISearchAndWatchPagination(t *testing.T) {
	t.Parallel()
	manager, known, _, _ := webTestQueue(t, true)
	store, err := queue.Open(known.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ watch, path string }{
		{"incoming", "/drop/report-one.csv"},
		{"archive", "/drop/report-two.csv"},
		{"incoming", "/drop/report-three.csv"},
		{"incoming", "/drop/newest-unrelated.csv"},
	} {
		if _, _, err := store.Enqueue(context.Background(), queue.EnqueueParams{WatchName: item.watch, Path: item.path}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	handler, err := newHandler(manager, testLogger(), testWebToken, "dev")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		query string
		id    int64
		more  bool
	}{
		{"search=REPORT&limit=1", 5, true},
		{"search=report&limit=1&offset=1", 4, true},
		{"search=report&watch=incoming&status=queued&limit=1", 5, true},
		{"search=report&watch=incoming&status=queued&limit=1&offset=1", 3, false},
		{"search=report&watch=archive&limit=1", 4, false},
		{"search=report&status=succeeded&limit=1", 0, false},
	} {
		t.Run(test.query, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "http://localhost/api/v1/queues/"+queueID(known.Identity)+"/jobs?"+test.query, nil)
			request.Header.Set("Authorization", "Bearer "+testWebToken)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			var result jobsResponse
			decodeResponse(t, response.Result(), http.StatusOK, &result)
			if test.id == 0 {
				if len(result.Jobs) != 0 || result.HasMore {
					t.Fatalf("expected empty result: %+v", result)
				}
			} else if len(result.Jobs) != 1 || result.Jobs[0].ID != test.id || result.HasMore != test.more {
				t.Fatalf("filtered page = %+v, want job %d and has_more %t", result, test.id, test.more)
			}
		})
	}
	if _, _, _, err := jobFilter(url.Values{"search": {strings.Repeat("x", 1025)}}); err == nil {
		t.Fatal("oversized search accepted")
	}
}
