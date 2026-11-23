package queue

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestWebhookSignatureCoversTimestampAndBody(t *testing.T) {
	t.Parallel()
	secret := "0123456789abcdef"
	body := []byte(`{"seq":1,"type":"job.succeeded"}`)
	stamp := time.Unix(1_700_000_000, 0).UTC()
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte("1700000000."))
	_, _ = mac.Write(body)
	want := "t=1700000000,v1=" + hex.EncodeToString(mac.Sum(nil))
	if got := SignWebhook(secret, body, stamp); got != want {
		t.Fatalf("signature = %s, want %s", got, want)
	}
}

func TestWebhooksDeliverMatchingEventsAndDeadLetter(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	store := openTestStore(t)
	store.now = func() time.Time { return now }

	matched, err := store.CreateWebhook(ctx, WebhookParams{
		URL: "https://agent.example/hook", Secret: "0123456789abcdef",
		Events: []string{"job.succeeded"}, MaxAttempts: 2, QueueID: "queue-1",
	})
	if err != nil || matched.ID == 0 {
		t.Fatal(err)
	}
	ignored, err := store.CreateWebhook(ctx, WebhookParams{
		URL: "https://agent.example/other", Secret: "abcdef0123456789", Events: []string{"job.failed"},
	})
	if err != nil {
		t.Fatal(err)
	}
	job, _, err := store.Enqueue(ctx, EnqueueParams{
		WatchName: "incoming", Path: "/drop/a.csv", ExternalID: "batch-7", MaxRetries: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	pending, err := store.ListWebhookDeliveries(ctx, matched.ID, "pending", 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("enqueue notified a succeeded-only webhook: %+v %v", pending, err)
	}
	claimed, err := store.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Succeed(ctx, claimed.ID, claimed.RunID); err != nil {
		t.Fatal(err)
	}
	pending, err = store.ListWebhookDeliveries(ctx, matched.ID, "", 10)
	if err != nil || len(pending) != 1 || pending[0].EventType != EventJobSucceeded || pending[0].Status != "pending" {
		t.Fatalf("deliveries = %+v %v", pending, err)
	}
	var payload deliveryBody
	if err := json.Unmarshal([]byte(pending[0].Payload), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.QueueID != "queue-1" || payload.JobID != job.ID || payload.ExternalID != "batch-7" || payload.Path != "/drop/a.csv" {
		t.Fatalf("payload = %+v", payload)
	}
	other, err := store.ListWebhookDeliveries(ctx, ignored.ID, "", 10)
	if err != nil || len(other) != 0 {
		t.Fatalf("unmatched webhook deliveries = %+v %v", other, err)
	}

	due, err := store.ClaimWebhookDeliveries(ctx, 8)
	if err != nil || len(due) != 1 || due[0].Secret != "0123456789abcdef" || due[0].URL != matched.URL {
		t.Fatalf("claim = %+v %v", due, err)
	}
	if again, err := store.ClaimWebhookDeliveries(ctx, 8); err != nil || len(again) != 0 {
		t.Fatalf("leased delivery was claimed again: %+v %v", again, err)
	}
	now = now.Add(webhookBackoff(1))
	if status, err := store.FinishWebhookDelivery(ctx, due[0].ID, false, "webhook returned HTTP 500"); err != nil || status != "pending" {
		t.Fatalf("retry status = %s %v", status, err)
	}
	now = now.Add(webhookBackoff(1) - time.Second)
	if early, err := store.ClaimWebhookDeliveries(ctx, 8); err != nil || len(early) != 0 {
		t.Fatalf("delivery claimed before backoff: %+v %v", early, err)
	}
	now = now.Add(time.Second)
	due, err = store.ClaimWebhookDeliveries(ctx, 8)
	if err != nil || len(due) != 1 || due[0].Attempts != 1 {
		t.Fatalf("second claim = %+v %v", due, err)
	}
	if status, err := store.FinishWebhookDelivery(ctx, due[0].ID, false, "webhook returned HTTP 500"); err != nil || status != "dead" {
		t.Fatalf("dead status = %s %v", status, err)
	}
	dead, err := store.ListWebhookDeliveries(ctx, matched.ID, "dead", 10)
	if err != nil || len(dead) != 1 || dead[0].Attempts != 2 || dead[0].LastError == "" {
		t.Fatalf("dead letter = %+v %v", dead, err)
	}

	hooks, err := store.ListWebhooks(ctx)
	if err != nil || len(hooks) != 2 {
		t.Fatalf("hooks = %+v %v", hooks, err)
	}
	if err := store.DeleteWebhook(ctx, matched.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListWebhookDeliveries(ctx, matched.ID, "", 10); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted webhook deliveries: %v", err)
	}
	var remaining int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM webhook_deliveries WHERE webhook_id = ?`, matched.ID).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("cascade left %d deliveries: %v", remaining, err)
	}
}

func TestWebhookValidationAndLimit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	for _, params := range []WebhookParams{
		{URL: "file:///tmp/hook", Secret: "0123456789abcdef"},
		{URL: "https://user:pass@agent.example/hook", Secret: "0123456789abcdef"},
		{URL: "https://agent.example/hook", Secret: "short"},
		{URL: "https://agent.example/hook", Secret: "0123456789abcdef", Events: []string{"job.nope"}},
		{URL: "https://agent.example/hook", Secret: "0123456789abcdef", MaxAttempts: 21},
	} {
		if _, err := store.CreateWebhook(ctx, params); err == nil {
			t.Fatalf("accepted %+v", params)
		}
	}
	for i := 0; i < MaxWebhooksPerQueue; i++ {
		if _, err := store.CreateWebhook(ctx, WebhookParams{
			URL: "https://agent.example/hook", Secret: "0123456789abcdef",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.CreateWebhook(ctx, WebhookParams{URL: "https://agent.example/hook", Secret: "0123456789abcdef"}); !errors.Is(err, ErrWebhookLimit) {
		t.Fatalf("limit error = %v", err)
	}
	path := filepath.Join(t.TempDir(), "reopen.db")
	fresh, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fresh.CreateWebhook(ctx, WebhookParams{URL: "https://agent.example/hook", Secret: "0123456789abcdef"}); err != nil {
		t.Fatal(err)
	}
	if err := fresh.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	hooks, err := reopened.ListWebhooks(ctx)
	if err != nil || len(hooks) != 1 || hooks[0].MaxAttempts != DefaultWebhookAttempts {
		t.Fatalf("reopened hooks = %+v %v", hooks, err)
	}
}
