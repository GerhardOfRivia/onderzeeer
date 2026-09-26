package queue

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// RemoveJob atomically deletes an unclaimed job and its cascading run, command,
// and resource-wait history. A concurrent claim either wins or sees no job.
func (s *Store) RemoveJob(ctx context.Context, id int64) error {
	if id <= 0 {
		return errors.New("queue: invalid job id")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("queue: begin job removal: %w", err)
	}
	defer rollback(tx)
	var removed int64
	err = tx.QueryRowContext(ctx, `DELETE FROM jobs WHERE id = ? AND status IN (?, ?, ?) RETURNING id`,
		id, StatusQueued, StatusSucceeded, StatusFailed).Scan(&removed)
	if errors.Is(err, sql.ErrNoRows) {
		var status Status
		err = tx.QueryRowContext(ctx, "SELECT status FROM jobs WHERE id = ?", id).Scan(&status)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("queue: inspect job for removal: %w", err)
		}
		return ErrJobActive
	}
	if err != nil {
		return fmt.Errorf("queue: remove job: %w", err)
	}
	return tx.Commit()
}
