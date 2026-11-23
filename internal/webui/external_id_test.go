package webui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/GerhardOfRivia/onderzeeer/internal/queue"
)

func TestJobsAPIFiltersExternalID(t *testing.T) {
	t.Parallel()
	manager, known, _, _ := webTestQueue(t, true)
	store, err := queue.Open(known.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	job, created, err := store.Enqueue(context.Background(), queue.EnqueueParams{
		WatchName: "incoming", Path: "/drop/tagged.csv", Fingerprint: "tagged", ExternalID: "batch-42",
	})
	if err != nil || !created {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	handler, err := newHandler(manager, testLogger(), testWebToken, "dev")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "http://localhost/api/v1/queues/"+queueID(known.Identity)+"/jobs?external_id=batch-42", nil)
	request.Header.Set("Authorization", "Bearer "+testWebToken)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var result jobsResponse
	decodeResponse(t, response.Result(), http.StatusOK, &result)
	if len(result.Jobs) != 1 || result.Jobs[0].ID != job.ID || result.Jobs[0].ExternalID != "batch-42" {
		t.Fatalf("filtered jobs = %+v", result.Jobs)
	}

	detail := httptest.NewRequest(http.MethodGet, "http://localhost/api/v1/queues/"+queueID(known.Identity)+"/jobs/"+strconv.FormatInt(job.ID, 10), nil)
	detail.Header.Set("Authorization", "Bearer "+testWebToken)
	recorded := httptest.NewRecorder()
	handler.ServeHTTP(recorded, detail)
	var jobDetail jobResponse
	decodeResponse(t, recorded.Result(), http.StatusOK, &jobDetail)
	if jobDetail.Job.ExternalID != "batch-42" {
		t.Fatalf("detail = %+v", jobDetail.Job)
	}

	if _, _, _, err := jobFilter(url.Values{"external_id": {strings.Repeat("x", queue.MaxExternalIDBytes+1)}}); err == nil {
		t.Fatal("oversized external_id filter accepted")
	}
}
