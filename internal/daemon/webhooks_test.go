package daemon

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GerhardOfRivia/onderzeeer/internal/config"
	"github.com/GerhardOfRivia/onderzeeer/internal/metrics"
	"github.com/GerhardOfRivia/onderzeeer/internal/queue"
)

func TestDeliverDuePostsSignatureAndDeadLetters(t *testing.T) {
	t.Parallel()
	secret := "super-secret-value"
	var mu sync.Mutex
	var gotBody []byte
	var got http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotBody, got = body, r.Header.Clone()
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	ctx := context.Background()
	store := openDaemonStore(t)
	hook, err := store.CreateWebhook(ctx, queue.WebhookParams{
		URL: server.URL, Secret: secret, Events: []string{queue.EventJobEnqueued}, MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Enqueue(ctx, queue.EnqueueParams{WatchName: "incoming", Path: "/drop/a.csv", ExternalID: "batch-7"}); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	before := metrics.Sample("onderzeeer_webhook_deliveries_total", map[string]string{"result": "succeeded"})
	if err := deliverDueWebhooks(ctx, store, server.Client(), logger); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	body, header := append([]byte(nil), gotBody...), got.Clone()
	mu.Unlock()
	if header.Get("X-onderzeeer-Event") != queue.EventJobEnqueued || header.Get("X-onderzeeer-Delivery") == "" {
		t.Fatalf("headers = %v", header)
	}
	if !signatureMatches(secret, body, header.Get("X-onderzeeer-Signature")) {
		t.Fatalf("signature %s body %s", header.Get("X-onderzeeer-Signature"), body)
	}
	if strings.Contains(logs.String(), secret) || strings.Contains(string(body), secret) {
		t.Fatal("secret was logged or sent in the body")
	}
	if metrics.Sample("onderzeeer_webhook_deliveries_total", map[string]string{"result": "succeeded"}) < before+1 {
		t.Fatal("successful delivery was not counted")
	}
	rows, err := store.ListWebhookDeliveries(ctx, hook.ID, "succeeded", 10)
	if err != nil || len(rows) != 1 || rows[0].Attempts != 1 {
		t.Fatalf("recorded delivery = %+v %v", rows, err)
	}

	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusBadGateway)
	}))
	defer failing.Close()
	dead, err := store.CreateWebhook(ctx, queue.WebhookParams{
		URL: failing.URL, Secret: secret, MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Enqueue(ctx, queue.EnqueueParams{WatchName: "incoming", Path: "/drop/b.csv"}); err != nil {
		t.Fatal(err)
	}
	deadBefore := metrics.Sample("onderzeeer_webhook_deliveries_total", map[string]string{"result": "dead"})
	if err := deliverDueWebhooks(ctx, store, failing.Client(), logger); err != nil {
		t.Fatal(err)
	}
	rows, err = store.ListWebhookDeliveries(ctx, dead.ID, "dead", 10)
	if err != nil || len(rows) != 1 || !strings.Contains(rows[0].LastError, "502") {
		t.Fatalf("dead letter = %+v %v", rows, err)
	}
	if metrics.Sample("onderzeeer_webhook_deliveries_total", map[string]string{"result": "dead"}) < deadBefore+1 {
		t.Fatal("dead letter was not counted")
	}
	if strings.Contains(logs.String(), secret) {
		t.Fatal("secret was logged")
	}
}

func TestRunDeliversEnqueuedFileWebhook(t *testing.T) {
	secret := "another-secret-value"
	received := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Header.Get("X-onderzeeer-Event") == queue.EventJobEnqueued && signatureMatches(secret, body, r.Header.Get("X-onderzeeer-Signature")) {
			select {
			case received <- struct{}{}:
			default:
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	directory := t.TempDir()
	incoming := filepath.Join(directory, "incoming")
	if err := os.Mkdir(incoming, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(incoming, "drop.csv"), []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(directory, "queue.db")
	store, err := queue.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateWebhook(context.Background(), queue.WebhookParams{
		URL: server.URL, Secret: secret, Events: []string{queue.EventJobEnqueued},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Queue:    config.QueueConfig{Workers: 1, RetryDelay: config.Duration{Duration: time.Millisecond}},
		Database: config.DatabaseConfig{Path: database},
		Watches: []config.WatchConfig{{
			Name: "incoming", Path: incoming, ProcessExisting: true,
			SettleFor: config.Duration{Duration: 10 * time.Millisecond},
			Pipeline:  []config.CommandConfig{{Name: "noop", Program: "/bin/true"}},
		}},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	var logs bytes.Buffer
	go func() {
		done <- Run(ctx, cfg, slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("daemon did not stop")
		}
	}()
	select {
	case <-received:
	case <-time.After(5 * time.Second):
		t.Fatal("file-drop webhook was not delivered")
	}
	if strings.Contains(logs.String(), secret) {
		t.Fatal("daemon log contained the webhook secret")
	}
}

func signatureMatches(secret string, body []byte, header string) bool {
	unix, hexPart, ok := strings.Cut(strings.TrimPrefix(header, "t="), ",v1=")
	if !ok {
		return false
	}
	seconds, err := strconv.ParseInt(unix, 10, 64)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(unix + "."))
	_, _ = mac.Write(body)
	if hex.EncodeToString(mac.Sum(nil)) != hexPart {
		return false
	}
	return seconds > 0
}

func openDaemonStore(t *testing.T) *queue.Store {
	t.Helper()
	store, err := queue.Open(filepath.Join(t.TempDir(), "queue.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
