package webui

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/GerhardOfRivia/onderzeeer/internal/queue"
)

func TestWebhookAPIHidesSecretAndListsDeliveries(t *testing.T) {
	t.Parallel()
	manager, known, _, _ := webTestQueue(t, true)
	handler, err := newHandler(manager, testLogger(), testWebToken, "dev")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	base := "/api/v1/queues/" + queueID(known.Identity) + "/webhooks"
	secret := "super-secret-value"

	unauth := httptest.NewRequest(http.MethodPost, server.URL+base, strings.NewReader(`{"url":"https://agent.example/hook","secret":"`+secret+`"}`))
	recorded := httptest.NewRecorder()
	handler.ServeHTTP(recorded, unauth)
	if recorded.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated create = %d", recorded.Code)
	}

	created := webhookMutation(t, server, http.MethodPost, base, `{"url":"https://agent.example/hook","secret":"`+secret+`","events":["job.enqueued"],"max_attempts":3}`)
	var hook webhookView
	decodeResponse(t, created, http.StatusCreated, &hook)
	body := createdBody(t, server, http.MethodPost, base, `{"url":"file:///tmp/x","secret":"`+secret+`"}`)
	if body.Code != http.StatusBadRequest {
		t.Fatalf("bad url = %d %s", body.Code, body.Body.String())
	}
	listed := httptest.NewRequest(http.MethodGet, server.URL+base, nil)
	listed.Header.Set("Authorization", "Bearer "+testWebToken)
	recorded = httptest.NewRecorder()
	handler.ServeHTTP(recorded, listed)
	raw, err := io.ReadAll(recorded.Body)
	if err != nil {
		t.Fatal(err)
	}
	if recorded.Code != http.StatusOK || strings.Contains(string(raw), secret) || !strings.Contains(string(raw), "https://agent.example/hook") {
		t.Fatalf("list = %d %s", recorded.Code, raw)
	}

	store, err := queue.Open(known.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Enqueue(context.Background(), queue.EnqueueParams{
		WatchName: "incoming", Path: "/drop/from-api.csv", ExternalID: "batch-9",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	deliveries := httptest.NewRequest(http.MethodGet, server.URL+base+"/"+strconv.FormatInt(hook.ID, 10)+"/deliveries?status=pending", nil)
	deliveries.Header.Set("Authorization", "Bearer "+testWebToken)
	recorded = httptest.NewRecorder()
	handler.ServeHTTP(recorded, deliveries)
	var page webhookDeliveriesResponse
	decodeResponse(t, recorded.Result(), http.StatusOK, &page)
	if len(page.Deliveries) != 1 || page.Deliveries[0].EventType != queue.EventJobEnqueued || !strings.Contains(page.Deliveries[0].Payload, "batch-9") || strings.Contains(page.Deliveries[0].Payload, secret) {
		t.Fatalf("deliveries = %+v", page.Deliveries)
	}

	removed := webhookMutation(t, server, http.MethodDelete, base+"/"+strconv.FormatInt(hook.ID, 10), "")
	var deleted deletedWebhookResponse
	decodeResponse(t, removed, http.StatusOK, &deleted)
	if deleted.ID != hook.ID {
		t.Fatalf("deleted = %+v", deleted)
	}
	missing := webhookMutation(t, server, http.MethodDelete, base+"/"+strconv.FormatInt(hook.ID, 10), "")
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("second delete = %d", missing.StatusCode)
	}
}

func webhookMutation(t *testing.T, server *httptest.Server, method, path, body string) *http.Response {
	t.Helper()
	response := createdBody(t, server, method, path, body)
	return response.Result()
}

func createdBody(t *testing.T, server *httptest.Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, server.URL+path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+testWebToken)
	request.Header.Set("X-onderzeeer-Web", "1")
	request.Header.Set("Origin", server.URL)
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/json")
	}
	recorded := httptest.NewRecorder()
	server.Config.Handler.ServeHTTP(recorded, request)
	return recorded
}
