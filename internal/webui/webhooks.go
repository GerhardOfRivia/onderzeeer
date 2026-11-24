package webui

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/GerhardOfRivia/onderzeeer/internal/control"
	"github.com/GerhardOfRivia/onderzeeer/internal/queue"
)

const maxWebhookBody = 8 << 10

type webhookRequest struct {
	URL         string   `json:"url"`
	Secret      string   `json:"secret"`
	Events      []string `json:"events"`
	MaxAttempts *int     `json:"max_attempts"`
}

type webhookView struct {
	ID          int64     `json:"id"`
	URL         string    `json:"url"`
	Events      []string  `json:"events"`
	MaxAttempts int       `json:"max_attempts"`
	CreatedAt   time.Time `json:"created_at"`
}

type webhooksResponse struct {
	Webhooks []webhookView `json:"webhooks"`
}

type webhookDeliveryView struct {
	ID            int64     `json:"id"`
	WebhookID     int64     `json:"webhook_id"`
	EventSeq      int64     `json:"event_seq"`
	DeliveryID    string    `json:"delivery_id"`
	EventType     string    `json:"event_type"`
	Payload       string    `json:"payload"`
	Status        string    `json:"status"`
	Attempts      int       `json:"attempts"`
	NextAttemptAt time.Time `json:"next_attempt_at"`
	LastError     string    `json:"last_error,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type webhookDeliveriesResponse struct {
	Deliveries []webhookDeliveryView `json:"deliveries"`
}

type deletedWebhookResponse struct {
	ID int64 `json:"id"`
}

func (api *apiServer) handleCreateWebhook(output http.ResponseWriter, request *http.Request) {
	var body webhookRequest
	if err := decodeMutation(output, request, &body, maxWebhookBody); err != nil {
		writeAPIError(output, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	known, ok := api.resolveQueue(request.PathValue("queueID"))
	if !ok {
		writeAPIError(output, http.StatusNotFound, "queue_not_found", "Queue not found")
		return
	}
	store, err := openKnownQueueWritable(request.Context(), known)
	if err != nil {
		writeQueueUnavailable(output, err)
		return
	}
	defer store.Close()
	attempts := 0
	if body.MaxAttempts != nil {
		attempts = *body.MaxAttempts
	}
	hook, err := store.CreateWebhook(request.Context(), queue.WebhookParams{
		URL: body.URL, Secret: body.Secret, Events: body.Events, MaxAttempts: attempts,
		QueueID: queueID(known.Identity),
	})
	if err != nil {
		writeWebhookError(output, err)
		return
	}
	writeJSON(output, http.StatusCreated, toWebhookView(hook))
}

func (api *apiServer) handleWebhooks(output http.ResponseWriter, request *http.Request) {
	known, ok := api.resolveQueue(request.PathValue("queueID"))
	if !ok {
		writeAPIError(output, http.StatusNotFound, "queue_not_found", "Queue not found")
		return
	}
	store, err := openKnownQueue(request.Context(), known)
	if err != nil {
		writeQueueUnavailable(output, err)
		return
	}
	defer store.Close()
	hooks, err := store.ListWebhooks(request.Context())
	if err != nil {
		writeAPIError(output, http.StatusServiceUnavailable, "queue_unavailable", err.Error())
		return
	}
	views := make([]webhookView, 0, len(hooks))
	for _, hook := range hooks {
		views = append(views, toWebhookView(hook))
	}
	writeJSON(output, http.StatusOK, webhooksResponse{Webhooks: views})
}

func (api *apiServer) handleDeleteWebhook(output http.ResponseWriter, request *http.Request) {
	if err := validateControlHeaders(request); err != nil {
		writeAPIError(output, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	known, ok := api.resolveQueue(request.PathValue("queueID"))
	if !ok {
		writeAPIError(output, http.StatusNotFound, "queue_not_found", "Queue not found")
		return
	}
	webhookID, err := positiveID(request.PathValue("webhookID"), "webhook")
	if err != nil {
		writeAPIError(output, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	store, err := openKnownQueueWritable(request.Context(), known)
	if err != nil {
		writeQueueUnavailable(output, err)
		return
	}
	defer store.Close()
	if err := store.DeleteWebhook(request.Context(), webhookID); err != nil {
		writeWebhookError(output, err)
		return
	}
	writeJSON(output, http.StatusOK, deletedWebhookResponse{ID: webhookID})
}

func (api *apiServer) handleWebhookDeliveries(output http.ResponseWriter, request *http.Request) {
	known, ok := api.resolveQueue(request.PathValue("queueID"))
	if !ok {
		writeAPIError(output, http.StatusNotFound, "queue_not_found", "Queue not found")
		return
	}
	webhookID, err := positiveID(request.PathValue("webhookID"), "webhook")
	if err != nil {
		writeAPIError(output, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	status := request.URL.Query().Get("status")
	switch status {
	case "", "pending", "succeeded", "dead":
	default:
		writeAPIError(output, http.StatusBadRequest, "invalid_request", "status must be pending, succeeded, or dead")
		return
	}
	store, err := openKnownQueue(request.Context(), known)
	if err != nil {
		writeQueueUnavailable(output, err)
		return
	}
	defer store.Close()
	deliveries, err := store.ListWebhookDeliveries(request.Context(), webhookID, status, 100)
	if err != nil {
		writeWebhookError(output, err)
		return
	}
	views := make([]webhookDeliveryView, 0, len(deliveries))
	for _, delivery := range deliveries {
		views = append(views, webhookDeliveryView{
			ID: delivery.ID, WebhookID: delivery.WebhookID, EventSeq: delivery.EventSeq,
			DeliveryID: delivery.DeliveryID, EventType: delivery.EventType, Payload: delivery.Payload,
			Status: delivery.Status, Attempts: delivery.Attempts, NextAttemptAt: delivery.NextAttemptAt,
			LastError: delivery.LastError, CreatedAt: delivery.CreatedAt, UpdatedAt: delivery.UpdatedAt,
		})
	}
	writeJSON(output, http.StatusOK, webhookDeliveriesResponse{Deliveries: views})
}

func openKnownQueueWritable(ctx context.Context, known control.KnownQueue) (*queue.Store, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, databasePath := range knownQueuePaths(known) {
		info, err := os.Stat(databasePath)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, errors.New("queue database is not a regular file")
		}
		return queue.Open(databasePath)
	}
	return nil, errQueueUninitialized
}

func toWebhookView(hook queue.Webhook) webhookView {
	events := append([]string(nil), hook.Events...)
	if events == nil {
		events = []string{}
	}
	return webhookView{
		ID: hook.ID, URL: hook.URL, Events: events, MaxAttempts: hook.MaxAttempts, CreatedAt: hook.CreatedAt,
	}
}

func writeWebhookError(output http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, queue.ErrNotFound):
		writeAPIError(output, http.StatusNotFound, "webhook_not_found", "Webhook not found")
	case errors.Is(err, queue.ErrWebhookLimit):
		writeAPIError(output, http.StatusConflict, "webhook_limit", err.Error())
	case errors.Is(err, errQueueUninitialized):
		writeAPIError(output, http.StatusConflict, "queue_uninitialized", "Queue has not been created yet")
	default:
		message := err.Error()
		if strings.HasPrefix(message, "queue: webhook") || strings.HasPrefix(message, "queue: unknown webhook") || strings.HasPrefix(message, "queue: duplicate webhook") || strings.HasPrefix(message, "queue: invalid delivery") {
			writeAPIError(output, http.StatusBadRequest, "invalid_request", message)
			return
		}
		writeAPIError(output, http.StatusServiceUnavailable, "queue_unavailable", message)
	}
}
