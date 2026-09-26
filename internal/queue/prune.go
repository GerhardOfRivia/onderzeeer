package queue

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const PrunedOutputMarker = "[onderzeeer: captured output pruned]\n"
const pruneBatchSize = 32

// Match the partial index exactly so repeat retention passes skip previously
// pruned and empty output without scanning their command rows or large streams.
const retainedOutputPredicate = "(stdout != '' OR stderr != '') AND (stdout != '" + PrunedOutputMarker + "' OR stderr != '')"

type PruneOptions struct {
	Before        time.Time `json:"before"`
	IncludeFailed bool      `json:"include_failed"`
	DryRun        bool      `json:"dry_run"`
}

type PruneResult struct {
	Commands    int64 `json:"commands"`
	OutputBytes int64 `json:"output_bytes"`
	DryRun      bool  `json:"dry_run"`
}

func (options PruneOptions) Validate(now time.Time) error {
	if options.Before.IsZero() || options.Before.After(now) {
		return errors.New("queue: pruning requires a completion cutoff in the past")
	}
	if !timeFromUnixNano(unixNano(options.Before)).Equal(options.Before) {
		return errors.New("queue: pruning cutoff is outside the supported timestamp range")
	}
	return nil
}

// PruneOutput removes only captured streams. Job fingerprints, run metadata,
// errors, and command metadata remain available. Each bounded batch commits
// independently; the result includes only committed work if a later batch fails.
func (s *Store) PruneOutput(ctx context.Context, options PruneOptions) (PruneResult, error) {
	result := PruneResult{DryRun: options.DryRun}
	if err := options.Validate(s.timestamp()); err != nil {
		return result, err
	}
	var upper int64
	if err := s.db.QueryRowContext(ctx, "SELECT COALESCE(MAX(id), 0) FROM command_executions").Scan(&upper); err != nil {
		return result, err
	}
	var after int64
	for after < upper {
		last, batch, err := s.pruneBatch(ctx, options, after, upper)
		if err != nil {
			return result, fmt.Errorf("queue: prune output: %w", err)
		}
		result.Commands += batch.Commands
		result.OutputBytes += batch.OutputBytes
		if last == after {
			break
		}
		after = last
	}
	return result, nil
}

func (s *Store) pruneBatch(ctx context.Context, options PruneOptions, after, upper int64) (int64, PruneResult, error) {
	var result PruneResult
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return after, result, err
	}
	defer conn.Close()
	if !options.DryRun {
		// Acquire the writer reservation before selecting candidates so a job
		// cannot become active between selection and clearing its output.
		if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
			return after, result, err
		}
		defer conn.ExecContext(context.Background(), "ROLLBACK")
	}
	failed := StatusSucceeded
	if options.IncludeFailed {
		failed = StatusFailed
	}
	rows, err := conn.QueryContext(ctx, `
SELECT c.id, length(CAST(c.stdout AS BLOB)) + length(CAST(c.stderr AS BLOB))
FROM command_executions AS c
JOIN runs AS r ON r.id = c.run_id
JOIN jobs AS j ON j.id = r.job_id
WHERE c.id > ? AND c.id <= ?
  AND j.status IN (?, ?) AND j.finished_at < ?
  AND c.status IN ('SUCCEEDED', 'FAILED')
  AND `+retainedOutputPredicate+`
ORDER BY c.id LIMIT ?`, after, upper, StatusSucceeded, failed, unixNano(options.Before), pruneBatchSize)
	if err != nil {
		return after, result, err
	}
	var ids []int64
	for rows.Next() {
		var id, size int64
		if err := rows.Scan(&id, &size); err != nil {
			rows.Close()
			return after, result, err
		}
		ids = append(ids, id)
		result.OutputBytes += size
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return after, PruneResult{}, err
	}
	if !options.DryRun {
		for _, id := range ids {
			if _, err := conn.ExecContext(ctx, "UPDATE command_executions SET stdout = ?, stderr = '' WHERE id = ?", PrunedOutputMarker, id); err != nil {
				return after, PruneResult{}, err
			}
		}
		if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
			return after, PruneResult{}, err
		}
	}
	result.Commands = int64(len(ids))
	if len(ids) > 0 {
		after = ids[len(ids)-1]
	}
	return after, result, nil
}

// Compact requires a quiescent queue. Managed callers additionally hold the
// lifecycle gate and refuse active instances; standalone writers must be stopped.
func (s *Store) Compact(ctx context.Context) error {
	var active int
	if err := s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM jobs WHERE status IN ('RUNNING', 'PENDING'))").Scan(&active); err != nil {
		return err
	}
	if active != 0 {
		return errors.New("queue: stop running and pending jobs before compacting")
	}
	info, err := s.Storage(ctx)
	if err != nil {
		return err
	}
	if info.Disk != nil && info.Disk.AvailableBytes < 2*uint64(info.DatabaseBytes+info.WALBytes) {
		return errors.New("queue: compaction requires free disk space of at least twice the database and WAL size")
	}
	if _, err := s.db.ExecContext(ctx, "VACUUM"); err != nil {
		return fmt.Errorf("queue: compact: %w", err)
	}
	var busy, log, checkpointed int
	err = s.db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &log, &checkpointed)
	if err != nil {
		return err
	}
	if busy != 0 {
		return errors.New("queue: database compacted, but WAL truncation is blocked by another connection; close readers and retry")
	}
	return nil
}
