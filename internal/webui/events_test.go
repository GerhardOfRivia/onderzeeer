package webui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/GerhardOfRivia/onderzeeer/internal/queue"
)

func TestEventsAPILongPollSSEAndResume(t *testing.T) {
	t.Parallel()
	manager, known, _, _ := webTestQueue(t, true)
	handler, err := newHandler(manager, testLogger(), testWebToken, "dev")
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/queues/" + queueID(known.Identity) + "/events"

	poll := httptest.NewRequest(http.MethodGet, "http://localhost"+path+"?timeout=0&after=0&stream=poll", nil)
	poll.Header.Set("Authorization", "Bearer "+testWebToken)
	poll.Header.Set("Accept", "text/event-stream")
	recorded := httptest.NewRecorder()
	handler.ServeHTTP(recorded, poll)
	var page eventsResponse
	decodeResponse(t, recorded.Result(), http.StatusOK, &page)
	if recorded.Header().Get("Content-Type") == "text/event-stream" || len(page.Events) < 4 || page.After != 0 || page.Next != page.Events[len(page.Events)-1].Seq {
		t.Fatalf("poll overrode stream selection: %s %+v", recorded.Header().Get("Content-Type"), page)
	}
	if page.Events[0].Type != queue.EventJobEnqueued || page.Events[0].QueueID != queueID(known.Identity) || page.Events[0].Path == "" {
		t.Fatalf("first event = %+v", page.Events[0])
	}

	sse := httptest.NewRequest(http.MethodGet, "http://localhost"+path+"?timeout=0", nil)
	sse.Header.Set("Authorization", "Bearer "+testWebToken)
	sse.Header.Set("Accept", "text/event-stream")
	recorded = httptest.NewRecorder()
	handler.ServeHTTP(recorded, sse)
	if recorded.Code != http.StatusOK || !strings.HasPrefix(recorded.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("sse status=%d type=%s body=%s", recorded.Code, recorded.Header().Get("Content-Type"), recorded.Body.String())
	}
	body := recorded.Body.String()
	if !strings.Contains(body, "id: 1\n") || !strings.Contains(body, "event: job.enqueued\n") || !strings.Contains(body, `"seq":1`) {
		t.Fatalf("sse body = %s", body)
	}

	resume := httptest.NewRequest(http.MethodGet, "http://localhost"+path+"?timeout=0&after=0", nil)
	resume.Header.Set("Authorization", "Bearer "+testWebToken)
	resume.Header.Set("Last-Event-ID", "3")
	recorded = httptest.NewRecorder()
	handler.ServeHTTP(recorded, resume)
	decodeResponse(t, recorded.Result(), http.StatusOK, &page)
	if len(page.Events) == 0 || page.Events[0].Seq <= 3 || page.After != 3 {
		t.Fatalf("resume = %+v", page)
	}

	for _, target := range []string{path + "?stream=neither", path + "?after=-1", path + "?timeout=61", path + "?timeout=soon"} {
		request := httptest.NewRequest(http.MethodGet, "http://localhost"+target, nil)
		request.Header.Set("Authorization", "Bearer "+testWebToken)
		recorded = httptest.NewRecorder()
		handler.ServeHTTP(recorded, request)
		if recorded.Code != http.StatusBadRequest {
			t.Fatalf("%s status=%d body=%s", target, recorded.Code, recorded.Body.String())
		}
	}
}

func TestEventsAPIWaitsForTheNextEvent(t *testing.T) {
	t.Parallel()
	manager, known, _, _ := webTestQueue(t, true)
	handler, err := newHandler(manager, testLogger(), testWebToken, "dev")
	if err != nil {
		t.Fatal(err)
	}
	store, err := queue.Open(known.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	existing, err := store.ListEvents(context.Background(), 0, 100)
	if err != nil || len(existing) == 0 {
		t.Fatal(err)
	}
	cursor := existing[len(existing)-1].Seq

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		request := httptest.NewRequest(http.MethodGet, "http://localhost/api/v1/queues/"+queueID(known.Identity)+"/events?stream=sse&timeout=2&after="+strconv.FormatInt(cursor, 10), nil)
		request.Header.Set("Authorization", "Bearer "+testWebToken)
		recorded := httptest.NewRecorder()
		handler.ServeHTTP(recorded, request)
		done <- recorded
	}()
	time.Sleep(100 * time.Millisecond)
	if _, _, err := store.Enqueue(context.Background(), queue.EnqueueParams{
		WatchName: "incoming", Path: "/drop/later.csv", Fingerprint: "later", ExternalID: "later-1",
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case recorded := <-done:
		if recorded.Code != http.StatusOK || !strings.Contains(recorded.Body.String(), "event: job.enqueued") || !strings.Contains(recorded.Body.String(), "later-1") {
			t.Fatalf("waited sse = %d %s", recorded.Code, recorded.Body.String())
		}
	case <-time.After(4 * time.Second):
		t.Fatal("event stream did not return")
	}
}

func TestPreferEventStream(t *testing.T) {
	t.Parallel()
	if preferEventStream("") || preferEventStream("*/*") || preferEventStream("application/json") {
		t.Fatal("json clients were treated as SSE")
	}
	if !preferEventStream("text/event-stream") || !preferEventStream("text/event-stream, application/json;q=0.9") {
		t.Fatal("event-stream was not preferred")
	}
	if preferEventStream("application/json, text/event-stream;q=0.1") {
		t.Fatal("lower-q event-stream won")
	}
}
