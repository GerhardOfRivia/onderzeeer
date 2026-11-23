package queue

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	// DefaultWebhookAttempts is the number of HTTP tries before a delivery is
	// dead-lettered. A process crash while a try is leased does not consume one.
	DefaultWebhookAttempts = 8
	// MaxWebhookAttempts is the largest per-webhook try budget.
	MaxWebhookAttempts = 20
	// MaxWebhooksPerQueue bounds the subscriptions consulted on every event.
	MaxWebhooksPerQueue = 20
	webhookLease        = 30 * time.Second
	webhookBackoffCap   = 5 * time.Minute
	minWebhookSecret    = 16
	maxWebhookSecret    = 256
	maxWebhookURL       = 2048
)

const webhooksSchema = `
CREATE TABLE IF NOT EXISTS webhooks (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    url          TEXT NOT NULL,
    secret       TEXT NOT NULL,
    events_json  TEXT NOT NULL,
    max_attempts INTEGER NOT NULL CHECK (max_attempts > 0),
    queue_id     TEXT NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS webhook_deliveries (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    webhook_id      INTEGER NOT NULL REFERENCES webhooks(id) ON DELETE CASCADE,
    event_seq       INTEGER NOT NULL,
    delivery_id     TEXT NOT NULL,
    event_type      TEXT NOT NULL,
    payload         TEXT NOT NULL,
    status          TEXT NOT NULL CHECK (status IN ('pending', 'succeeded', 'dead')),
    attempts        INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at INTEGER NOT NULL,
    last_error      TEXT NOT NULL DEFAULT '',
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL,
    UNIQUE (webhook_id, event_seq)
);

CREATE INDEX IF NOT EXISTS webhook_deliveries_due_idx
    ON webhook_deliveries (status, next_attempt_at, id);
`

// ErrWebhookLimit means the queue already has MaxWebhooksPerQueue subscriptions.
var ErrWebhookLimit = errors.New("webhook limit reached")

// Webhook is a subscription. Secret is never included; callers that need it
// for delivery use WebhookAttempt.
type Webhook struct {
	ID          int64
	URL         string
	Events      []string
	MaxAttempts int
	QueueID     string
	CreatedAt   time.Time
}

// WebhookParams is a new subscription. An empty Events list matches every
// event type. QueueID is copied into each delivery payload.
type WebhookParams struct {
	URL         string
	Secret      string
	Events      []string
	MaxAttempts int
	QueueID     string
}

// WebhookDelivery is one durable attempt record without the signing secret.
type WebhookDelivery struct {
	ID            int64
	WebhookID     int64
	EventSeq      int64
	DeliveryID    string
	EventType     string
	Payload       string
	Status        string
	Attempts      int
	NextAttemptAt time.Time
	LastError     string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// WebhookAttempt is a leased delivery plus the material needed to send it.
// Secret must not be logged.
type WebhookAttempt struct {
	ID          int64
	WebhookID   int64
	EventSeq    int64
	DeliveryID  string
	EventType   string
	Payload     string
	Attempts    int
	MaxAttempts int
	URL         string
	Secret      string
}

func migrateWebhooks(db *sql.DB) error {
	if _, err := db.Exec(webhooksSchema); err != nil {
		return fmt.Errorf("create webhooks: %w", err)
	}
	return nil
}

// SignWebhook returns the X-onderzeeer-Signature value for body.
// The signed string is the decimal unix timestamp, a period, and the exact
// request body bytes: "<unix>.<body>". The header is "t=<unix>,v1=<hex>"
// with a lowercase hex HMAC-SHA256.
func SignWebhook(secret string, body []byte, timestamp time.Time) string {
	unix := timestamp.Unix()
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = fmt.Fprintf(mac, "%d.", unix)
	_, _ = mac.Write(body)
	return fmt.Sprintf("t=%d,v1=%s", unix, hex.EncodeToString(mac.Sum(nil)))
}

// CreateWebhook stores a subscription. The secret is kept for signing and is
// not returned.
func (s *Store) CreateWebhook(ctx context.Context, params WebhookParams) (Webhook, error) {
	normalized, err := normalizeWebhook(params)
	if err != nil {
		return Webhook{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Webhook{}, fmt.Errorf("queue: begin webhook: %w", err)
	}
	defer rollback(tx)
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM webhooks`).Scan(&count); err != nil {
		return Webhook{}, fmt.Errorf("queue: count webhooks: %w", err)
	}
	if count >= MaxWebhooksPerQueue {
		return Webhook{}, ErrWebhookLimit
	}
	now := s.timestamp()
	eventsJSON, err := json.Marshal(normalized.Events)
	if err != nil {
		return Webhook{}, fmt.Errorf("queue: encode webhook events: %w", err)
	}
	var id int64
	if err := tx.QueryRowContext(ctx, `
INSERT INTO webhooks (url, secret, events_json, max_attempts, queue_id, created_at)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING id`,
		normalized.URL, normalized.Secret, string(eventsJSON), normalized.MaxAttempts, normalized.QueueID, unixNano(now),
	).Scan(&id); err != nil {
		return Webhook{}, fmt.Errorf("queue: insert webhook: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Webhook{}, fmt.Errorf("queue: commit webhook: %w", err)
	}
	normalized.Secret = ""
	return Webhook{
		ID: id, URL: normalized.URL, Events: append([]string(nil), normalized.Events...),
		MaxAttempts: normalized.MaxAttempts, QueueID: normalized.QueueID, CreatedAt: now,
	}, nil
}

// ListWebhooks returns subscriptions in creation order. Secrets are not loaded.
func (s *Store) ListWebhooks(ctx context.Context) ([]Webhook, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, url, events_json, max_attempts, queue_id, created_at
FROM webhooks
ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("queue: list webhooks: %w", err)
	}
	defer rows.Close()
	hooks := make([]Webhook, 0)
	for rows.Next() {
		hook, err := scanWebhook(rows)
		if err != nil {
			return nil, fmt.Errorf("queue: scan webhook: %w", err)
		}
		hooks = append(hooks, hook)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("queue: list webhooks: %w", err)
	}
	return hooks, nil
}

// DeleteWebhook removes a subscription and, through the foreign key, its
// delivery history.
func (s *Store) DeleteWebhook(ctx context.Context, id int64) error {
	if id <= 0 {
		return fmt.Errorf("queue: delete webhook: %w", ErrNotFound)
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM webhooks WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("queue: delete webhook: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("queue: delete webhook: %w", err)
	}
	if count == 0 {
		return fmt.Errorf("queue: delete webhook: %w", ErrNotFound)
	}
	return nil
}

// ListWebhookDeliveries returns delivery records for one webhook, oldest first.
// status filters pending, succeeded, or dead. An empty status returns all.
func (s *Store) ListWebhookDeliveries(ctx context.Context, webhookID int64, status string, limit int) ([]WebhookDelivery, error) {
	if webhookID <= 0 {
		return nil, fmt.Errorf("queue: list deliveries: %w", ErrNotFound)
	}
	switch status {
	case "", "pending", "succeeded", "dead":
	default:
		return nil, fmt.Errorf("queue: invalid delivery status %q", status)
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 200 {
		limit = 200
	}
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM webhooks WHERE id = ?`, webhookID).Scan(&exists); err != nil {
		return nil, fmt.Errorf("queue: list deliveries: %w", err)
	}
	if exists == 0 {
		return nil, fmt.Errorf("queue: list deliveries: %w", ErrNotFound)
	}
	query := `
SELECT id, webhook_id, event_seq, delivery_id, event_type, payload, status,
       attempts, next_attempt_at, last_error, created_at, updated_at
FROM webhook_deliveries
WHERE webhook_id = ?`
	args := []any{webhookID}
	if status != "" {
		query += ` AND status = ?`
		args = append(args, status)
	}
	query += ` ORDER BY id LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("queue: list deliveries: %w", err)
	}
	defer rows.Close()
	deliveries := make([]WebhookDelivery, 0)
	for rows.Next() {
		delivery, err := scanDelivery(rows)
		if err != nil {
			return nil, fmt.Errorf("queue: scan delivery: %w", err)
		}
		deliveries = append(deliveries, delivery)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("queue: list deliveries: %w", err)
	}
	return deliveries, nil
}

// ClaimWebhookDeliveries leases due pending deliveries. The lease keeps a
// crashed sender from double-sending until it expires, and it does not count
// as an attempt.
func (s *Store) ClaimWebhookDeliveries(ctx context.Context, limit int) ([]WebhookAttempt, error) {
	if limit <= 0 {
		limit = 8
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("queue: begin webhook claim: %w", err)
	}
	defer rollback(tx)
	now := s.timestamp()
	rows, err := tx.QueryContext(ctx, `
SELECT d.id, d.webhook_id, d.event_seq, d.delivery_id, d.event_type, d.payload, d.attempts,
       w.url, w.secret, w.max_attempts
FROM webhook_deliveries AS d
JOIN webhooks AS w ON w.id = d.webhook_id
WHERE d.status = 'pending' AND d.next_attempt_at <= ?
ORDER BY d.id
LIMIT ?`, unixNano(now), limit)
	if err != nil {
		return nil, fmt.Errorf("queue: list due webhooks: %w", err)
	}
	attempts := make([]WebhookAttempt, 0)
	for rows.Next() {
		var attempt WebhookAttempt
		if err := rows.Scan(
			&attempt.ID, &attempt.WebhookID, &attempt.EventSeq, &attempt.DeliveryID, &attempt.EventType,
			&attempt.Payload, &attempt.Attempts, &attempt.URL, &attempt.Secret, &attempt.MaxAttempts,
		); err != nil {
			rows.Close()
			return nil, fmt.Errorf("queue: scan due webhook: %w", err)
		}
		attempts = append(attempts, attempt)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("queue: list due webhooks: %w", err)
	}
	rows.Close()
	lease := unixNano(now.Add(webhookLease))
	for _, attempt := range attempts {
		result, err := tx.ExecContext(ctx, `
UPDATE webhook_deliveries
SET next_attempt_at = ?, updated_at = ?
WHERE id = ? AND status = 'pending'`, lease, unixNano(now), attempt.ID)
		if err != nil {
			return nil, fmt.Errorf("queue: lease webhook delivery: %w", err)
		}
		if err := requireOneRow(result); err != nil {
			return nil, fmt.Errorf("queue: lease webhook delivery: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("queue: commit webhook claim: %w", err)
	}
	return attempts, nil
}

// FinishWebhookDelivery records the outcome of one leased try. success marks
// the row succeeded. A failure increments the try count, schedules exponential
// backoff capped at five minutes, and dead-letters the row once max attempts
// is reached.
func (s *Store) FinishWebhookDelivery(ctx context.Context, id int64, success bool, cause string) (string, error) {
	if id <= 0 {
		return "", fmt.Errorf("queue: finish webhook: %w", ErrNotFound)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("queue: begin webhook finish: %w", err)
	}
	defer rollback(tx)
	var attempts, maxAttempts int
	err = tx.QueryRowContext(ctx, `
SELECT d.attempts, w.max_attempts
FROM webhook_deliveries AS d
JOIN webhooks AS w ON w.id = d.webhook_id
WHERE d.id = ? AND d.status = 'pending'`, id).Scan(&attempts, &maxAttempts)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("queue: finish webhook: %w", ErrNotFound)
	}
	if err != nil {
		return "", fmt.Errorf("queue: read webhook delivery: %w", err)
	}
	now := s.timestamp()
	tries := attempts + 1
	status := "succeeded"
	next := now
	lastError := ""
	if !success {
		lastError = truncateWebhookError(cause)
		if tries >= maxAttempts {
			status = "dead"
		} else {
			status = "pending"
			next = now.Add(webhookBackoff(tries))
		}
	}
	result, err := tx.ExecContext(ctx, `
UPDATE webhook_deliveries
SET status = ?, attempts = ?, next_attempt_at = ?, last_error = ?, updated_at = ?
WHERE id = ? AND status = 'pending'`, status, tries, unixNano(next), lastError, unixNano(now), id)
	if err != nil {
		return "", fmt.Errorf("queue: update webhook delivery: %w", err)
	}
	if err := requireOneRow(result); err != nil {
		return "", fmt.Errorf("queue: update webhook delivery: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("queue: commit webhook finish: %w", err)
	}
	return status, nil
}

func (s *Store) queueDeliveries(ctx context.Context, tx *sql.Tx, seq int64, now time.Time, eventType string, job Job) error {
	rows, err := tx.QueryContext(ctx, `SELECT id, events_json, queue_id FROM webhooks ORDER BY id`)
	if err != nil {
		return fmt.Errorf("queue: list webhooks for event: %w", err)
	}
	type match struct {
		id      int64
		queueID string
	}
	matches := make([]match, 0)
	for rows.Next() {
		var (
			id         int64
			eventsJSON string
			queueID    string
		)
		if err := rows.Scan(&id, &eventsJSON, &queueID); err != nil {
			rows.Close()
			return fmt.Errorf("queue: scan webhook for event: %w", err)
		}
		if webhookWants(eventsJSON, eventType) {
			matches = append(matches, match{id: id, queueID: queueID})
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("queue: list webhooks for event: %w", err)
	}
	rows.Close()
	for _, hook := range matches {
		payload, err := json.Marshal(deliveryBody{
			Seq: seq, Type: eventType, QueueID: hook.queueID, JobID: job.ID, Path: job.Path,
			Status: job.Status, Attempts: job.Attempts, ExternalID: job.ExternalID,
			CreatedAt: now, JobCreatedAt: job.CreatedAt, UpdatedAt: job.UpdatedAt,
			StartedAt: job.StartedAt, FinishedAt: job.FinishedAt,
		})
		if err != nil {
			return fmt.Errorf("queue: encode webhook payload: %w", err)
		}
		deliveryID, err := newDeliveryID()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO webhook_deliveries (
    webhook_id, event_seq, delivery_id, event_type, payload, status,
    attempts, next_attempt_at, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, 'pending', 0, ?, ?, ?)`,
			hook.id, seq, deliveryID, eventType, string(payload), unixNano(now), unixNano(now), unixNano(now),
		); err != nil {
			return fmt.Errorf("queue: insert webhook delivery: %w", err)
		}
	}
	return nil
}

type deliveryBody struct {
	Seq          int64      `json:"seq"`
	Type         string     `json:"type"`
	QueueID      string     `json:"queue_id,omitempty"`
	JobID        int64      `json:"job_id"`
	Path         string     `json:"path"`
	Status       Status     `json:"status"`
	Attempts     int        `json:"attempts"`
	ExternalID   string     `json:"external_id,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	JobCreatedAt time.Time  `json:"job_created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
}

func normalizeWebhook(params WebhookParams) (WebhookParams, error) {
	rawURL := strings.TrimSpace(params.URL)
	if rawURL == "" || len(rawURL) > maxWebhookURL || strings.ContainsRune(rawURL, 0) {
		return WebhookParams{}, errors.New("queue: webhook url must be an http(s) URL of at most 2048 bytes")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") {
		return WebhookParams{}, errors.New("queue: webhook url must be http or https, with a host and without user info or a fragment")
	}
	secret := params.Secret
	if strings.ContainsRune(secret, 0) || len(secret) < minWebhookSecret || len(secret) > maxWebhookSecret {
		return WebhookParams{}, fmt.Errorf("queue: webhook secret must be %d to %d bytes and cannot contain NUL", minWebhookSecret, maxWebhookSecret)
	}
	events := make([]string, 0, len(params.Events))
	seen := map[string]bool{}
	for _, event := range params.Events {
		event = strings.TrimSpace(event)
		if event == "" {
			continue
		}
		if !validWebhookEvent(event) {
			return WebhookParams{}, fmt.Errorf("queue: unknown webhook event %q", event)
		}
		if seen[event] {
			return WebhookParams{}, fmt.Errorf("queue: duplicate webhook event %q", event)
		}
		seen[event] = true
		events = append(events, event)
	}
	attempts := params.MaxAttempts
	if attempts == 0 {
		attempts = DefaultWebhookAttempts
	}
	if attempts < 1 || attempts > MaxWebhookAttempts {
		return WebhookParams{}, fmt.Errorf("queue: webhook max attempts must be from 1 to %d", MaxWebhookAttempts)
	}
	queueID := strings.TrimSpace(params.QueueID)
	if len(queueID) > 128 || strings.ContainsRune(queueID, 0) {
		return WebhookParams{}, errors.New("queue: webhook queue id is invalid")
	}
	params.URL = rawURL
	params.Events = events
	params.MaxAttempts = attempts
	params.QueueID = queueID
	return params, nil
}

func validWebhookEvent(event string) bool {
	switch event {
	case EventJobEnqueued, EventJobStarted, EventJobSucceeded, EventJobFailed, EventJobRetried, EventJobInterrupted:
		return true
	default:
		return false
	}
}

func webhookWants(eventsJSON, eventType string) bool {
	eventsJSON = strings.TrimSpace(eventsJSON)
	if eventsJSON == "" || eventsJSON == "[]" || eventsJSON == "null" {
		return true
	}
	var events []string
	if err := json.Unmarshal([]byte(eventsJSON), &events); err != nil {
		return false
	}
	if len(events) == 0 {
		return true
	}
	for _, event := range events {
		if event == eventType {
			return true
		}
	}
	return false
}

func webhookBackoff(tries int) time.Duration {
	if tries < 1 {
		tries = 1
	}
	if tries > 8 {
		tries = 8
	}
	delay := time.Second << tries
	if delay <= 0 || delay > webhookBackoffCap {
		return webhookBackoffCap
	}
	return delay
}

func truncateWebhookError(cause string) string {
	cause = strings.TrimSpace(strings.ReplaceAll(cause, "\n", " "))
	if len(cause) > 300 {
		cause = cause[:300]
	}
	return cause
}

func newDeliveryID() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("queue: delivery id: %w", err)
	}
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16]), nil
}

func scanWebhook(row scanner) (Webhook, error) {
	var (
		hook       Webhook
		eventsJSON string
		createdAt  int64
	)
	if err := row.Scan(&hook.ID, &hook.URL, &eventsJSON, &hook.MaxAttempts, &hook.QueueID, &createdAt); err != nil {
		return Webhook{}, err
	}
	if eventsJSON != "" {
		if err := json.Unmarshal([]byte(eventsJSON), &hook.Events); err != nil {
			return Webhook{}, fmt.Errorf("decode webhook events: %w", err)
		}
	}
	if hook.Events == nil {
		hook.Events = []string{}
	}
	hook.CreatedAt = timeFromUnixNano(createdAt)
	return hook, nil
}

func scanDelivery(row scanner) (WebhookDelivery, error) {
	var (
		delivery                   WebhookDelivery
		nextAt, createdAt, updated int64
	)
	if err := row.Scan(
		&delivery.ID, &delivery.WebhookID, &delivery.EventSeq, &delivery.DeliveryID, &delivery.EventType,
		&delivery.Payload, &delivery.Status, &delivery.Attempts, &nextAt, &delivery.LastError, &createdAt, &updated,
	); err != nil {
		return WebhookDelivery{}, err
	}
	delivery.NextAttemptAt = timeFromUnixNano(nextAt)
	delivery.CreatedAt = timeFromUnixNano(createdAt)
	delivery.UpdatedAt = timeFromUnixNano(updated)
	return delivery, nil
}
