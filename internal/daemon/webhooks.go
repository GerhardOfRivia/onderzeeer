package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/GerhardOfRivia/onderzeeer/internal/metrics"
	"github.com/GerhardOfRivia/onderzeeer/internal/queue"
)

const webhookHTTPTimeout = 10 * time.Second

// deliverWebhooks posts due event deliveries until ctx is canceled. Delivery
// rows are claimed from SQLite first, so a crash retries after the lease
// instead of losing the event. Redirects are not followed.
func deliverWebhooks(ctx context.Context, store *queue.Store, logger *slog.Logger) {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	client := &http.Client{
		Timeout: webhookHTTPTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		if err := deliverDueWebhooks(ctx, store, client, logger); err != nil && ctx.Err() == nil {
			logger.Warn("webhook delivery failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func deliverDueWebhooks(ctx context.Context, store *queue.Store, client *http.Client, logger *slog.Logger) error {
	attempts, err := store.ClaimWebhookDeliveries(ctx, 8)
	if err != nil || len(attempts) == 0 {
		return err
	}
	var sendErr error
	for _, attempt := range attempts {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		status, cause := postWebhook(ctx, client, attempt)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		result, finishErr := store.FinishWebhookDelivery(ctx, attempt.ID, status >= 200 && status < 300, cause)
		if finishErr != nil {
			sendErr = errors.Join(sendErr, finishErr)
			continue
		}
		switch result {
		case "succeeded":
			metrics.WebhookDelivery("succeeded")
			logger.Debug("delivered webhook", "webhook_id", attempt.WebhookID, "delivery_id", attempt.DeliveryID, "event", attempt.EventType)
		case "dead":
			metrics.WebhookDelivery("dead")
			logger.Warn("webhook delivery dead-lettered", "webhook_id", attempt.WebhookID, "delivery_id", attempt.DeliveryID, "event", attempt.EventType, "error", cause)
		default:
			metrics.WebhookDelivery("retry")
			logger.Warn("webhook delivery will retry", "webhook_id", attempt.WebhookID, "delivery_id", attempt.DeliveryID, "event", attempt.EventType, "error", cause)
		}
	}
	return sendErr
}

func postWebhook(ctx context.Context, client *http.Client, attempt queue.WebhookAttempt) (int, string) {
	body := []byte(attempt.Payload)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, attempt.URL, strings.NewReader(attempt.Payload))
	if err != nil {
		return 0, "invalid webhook url"
	}
	timestamp := time.Now().UTC()
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "onderzeeer-webhook")
	request.Header.Set("X-onderzeeer-Event", attempt.EventType)
	request.Header.Set("X-onderzeeer-Delivery", attempt.DeliveryID)
	request.Header.Set("X-onderzeeer-Signature", queue.SignWebhook(attempt.Secret, body, timestamp))
	response, err := client.Do(request)
	if err != nil {
		return 0, "request failed"
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return response.StatusCode, fmt.Sprintf("webhook returned HTTP %d", response.StatusCode)
	}
	return response.StatusCode, ""
}
