package queue

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Event type names are stable API values. Agents and webhooks match on them.
const (
	EventJobEnqueued    = "job.enqueued"
	EventJobStarted     = "job.started"
	EventJobSucceeded   = "job.succeeded"
	EventJobFailed      = "job.failed"
	EventJobRetried     = "job.retried"
	EventJobInterrupted = "job.interrupted"
)

// DefaultEventRetention is the number of newest events kept per queue.
// Older rows are deleted. Sequence numbers are not reused.
const DefaultEventRetention = 10000

const eventsSchema = `
CREATE TABLE IF NOT EXISTS queue_events (
    seq            INTEGER PRIMARY KEY AUTOINCREMENT,
    type           TEXT NOT NULL,
    job_id         INTEGER NOT NULL,
    path           TEXT NOT NULL,
    status         TEXT NOT NULL,
    attempts       INTEGER NOT NULL,
    external_id    TEXT NOT NULL DEFAULT '',
    created_at     INTEGER NOT NULL,
    job_created_at INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL,
    started_at     INTEGER,
    finished_at    INTEGER
);`

// Event is one durable queue transition. Seq is monotonic for the database
// and survives restarts. CreatedAt is when the event was recorded.
type Event struct {
	Seq          int64
	Type         string
	JobID        int64
	Path         string
	Status       Status
	Attempts     int
	ExternalID   string
	CreatedAt    time.Time
	JobCreatedAt time.Time
	UpdatedAt    time.Time
	StartedAt    *time.Time
	FinishedAt   *time.Time
}

func migrateEvents(db *sql.DB) error {
	if _, err := db.Exec(eventsSchema); err != nil {
		return fmt.Errorf("create queue_events: %w", err)
	}
	return nil
}

func (s *Store) recordJobEvent(ctx context.Context, tx *sql.Tx, now time.Time, eventType string, jobID int64) error {
	job, err := scanJob(tx.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM jobs WHERE id = ?`, jobID))
	if err != nil {
		return fmt.Errorf("queue: read job for event: %w", err)
	}
	return s.insertEvent(ctx, tx, now, eventType, job)
}

func (s *Store) insertEvent(ctx context.Context, tx *sql.Tx, now time.Time, eventType string, job Job) error {
	var seq int64
	err := tx.QueryRowContext(ctx, `
INSERT INTO queue_events (
    type, job_id, path, status, attempts, external_id,
    created_at, job_created_at, updated_at, started_at, finished_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING seq`,
		eventType,
		job.ID,
		job.Path,
		string(job.Status),
		job.Attempts,
		job.ExternalID,
		unixNano(now),
		unixNano(job.CreatedAt),
		unixNano(job.UpdatedAt),
		nullableUnix(job.StartedAt),
		nullableUnix(job.FinishedAt),
	).Scan(&seq)
	if err != nil {
		return fmt.Errorf("queue: insert event: %w", err)
	}
	// Drop expired rows in the same transaction on a coarse cadence so a busy
	// queue cannot grow without bound between maintenance passes. AUTOINCREMENT
	// does not reuse the deleted sequence numbers.
	if seq > DefaultEventRetention && seq%1000 == 0 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM queue_events WHERE seq <= ?`, seq-DefaultEventRetention); err != nil {
			return fmt.Errorf("queue: prune events: %w", err)
		}
	}
	return s.queueDeliveries(ctx, tx, seq, now, eventType, job)
}

// ListEvents returns events with seq greater than after, oldest first.
func (s *Store) ListEvents(ctx context.Context, after int64, limit int) ([]Event, error) {
	if after < 0 {
		return nil, errors.New("queue: event cursor cannot be negative")
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT seq, type, job_id, path, status, attempts, external_id,
       created_at, job_created_at, updated_at, started_at, finished_at
FROM queue_events
WHERE seq > ?
ORDER BY seq
LIMIT ?`, after, limit)
	if err != nil {
		return nil, fmt.Errorf("queue: list events: %w", err)
	}
	defer rows.Close()
	events := make([]Event, 0)
	for rows.Next() {
		event, err := scanEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("queue: scan event: %w", err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("queue: list events: %w", err)
	}
	return events, nil
}

// PruneEvents deletes events older than the newest keep rows. keep less than
// 1 uses DefaultEventRetention. Sequence numbers already issued are not reused.
func (s *Store) PruneEvents(ctx context.Context, keep int) (int64, error) {
	if keep <= 0 {
		keep = DefaultEventRetention
	}
	result, err := s.db.ExecContext(ctx, `
DELETE FROM queue_events
WHERE seq <= COALESCE((SELECT MAX(seq) FROM queue_events), 0) - ?`, keep)
	if err != nil {
		return 0, fmt.Errorf("queue: prune events: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("queue: count pruned events: %w", err)
	}
	return count, nil
}

func scanEvent(row scanner) (Event, error) {
	var (
		event                              Event
		status                             string
		createdAt, jobCreatedAt, updatedAt int64
		startedAt, finishedAt              sql.NullInt64
	)
	if err := row.Scan(
		&event.Seq,
		&event.Type,
		&event.JobID,
		&event.Path,
		&status,
		&event.Attempts,
		&event.ExternalID,
		&createdAt,
		&jobCreatedAt,
		&updatedAt,
		&startedAt,
		&finishedAt,
	); err != nil {
		return Event{}, err
	}
	event.Status = Status(status)
	event.CreatedAt = timeFromUnixNano(createdAt)
	event.JobCreatedAt = timeFromUnixNano(jobCreatedAt)
	event.UpdatedAt = timeFromUnixNano(updatedAt)
	event.StartedAt = nullableTime(startedAt)
	event.FinishedAt = nullableTime(finishedAt)
	return event, nil
}

func nullableUnix(value *time.Time) any {
	if value == nil {
		return nil
	}
	return unixNano(*value)
}
