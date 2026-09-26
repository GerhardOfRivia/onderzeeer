package control

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

type PurgeFailure struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Error string `json:"error"`
}

// PurgeResult reports partial progress so filesystem failures never hide
// queues that have already been removed. Failed removals may be retried.
type PurgeResult struct {
	Removed  []Instance     `json:"removed"`
	Failures []PurgeFailure `json:"failures"`
}

// PurgeInactive permanently removes exited and failed registrations and their
// daemon-owned databases, including job history and SQLite sidecar files.
// Starts and writable API operations share the gate; runtime finalization and
// shutdown share mu. Active instances therefore cannot race this selection.
func (manager *Manager) PurgeInactive(ctx context.Context) (PurgeResult, error) {
	return manager.purgeInactive(ctx, nil)
}

// PurgeSelected removes only the inactive snapshots a caller has reviewed.
// An empty selection removes nothing; changed or restarted instances are kept.
func (manager *Manager) PurgeSelected(ctx context.Context, instances []Instance) (PurgeResult, error) {
	selection := make(map[string]Instance, len(instances))
	for _, instance := range instances {
		selection[instance.ID] = instance
	}
	return manager.purgeInactive(ctx, selection)
}

func (manager *Manager) purgeInactive(ctx context.Context, selection map[string]Instance) (PurgeResult, error) {
	result := PurgeResult{Removed: []Instance{}, Failures: []PurgeFailure{}}
	if ctx == nil {
		return result, errors.New("control: purge context is required")
	}
	if err := manager.acquireStartGate(ctx); err != nil {
		return result, err
	}
	defer manager.releaseStartGate()
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.shuttingDown || manager.ctx.Err() != nil {
		return result, ErrShuttingDown
	}
	if manager.registry == nil {
		return result, errors.New("control: purge requires a persistent daemon state directory")
	}
	var ids []string
	for id, runtime := range manager.instances {
		if selection != nil {
			if _, selected := selection[id]; selected {
				ids = append(ids, id)
			}
		} else if !runtime.view.Active() {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		runtime := manager.instances[id]
		if selection != nil && (runtime.view.Active() || !samePurgeSnapshot(runtime.view, selection[id])) {
			result.Failures = append(result.Failures, PurgeFailure{ID: id, Name: runtime.view.Name, Error: "queue changed since confirmation; review it and run system --purge again"})
			continue
		}
		if err := manager.purgeRegisteredLocked(ctx, runtime); err != nil {
			result.Failures = append(result.Failures, PurgeFailure{ID: id, Name: runtime.view.Name, Error: err.Error()})
			continue
		}
		result.Removed = append(result.Removed, cloneInstance(runtime.view))
		delete(manager.instances, id)
		delete(manager.names, runtime.view.Name)
		delete(manager.knownQueues, runtime.view.DatabasePath)
	}
	return result, nil
}

func samePurgeSnapshot(current, expected Instance) bool {
	finishedEqual := current.FinishedAt == nil && expected.FinishedAt == nil ||
		current.FinishedAt != nil && expected.FinishedAt != nil && current.FinishedAt.Equal(*expected.FinishedAt)
	return current.ID == expected.ID && current.Name == expected.Name &&
		current.ConfigPath == expected.ConfigPath && current.ConfigHash == expected.ConfigHash &&
		current.DatabasePath == expected.DatabasePath && current.State == expected.State &&
		current.DesiredState == expected.DesiredState && current.Error == expected.Error &&
		current.CreatedAt.Equal(expected.CreatedAt) && current.StartedAt.Equal(expected.StartedAt) && finishedEqual
}

func (manager *Manager) purgeRegisteredLocked(ctx context.Context, runtime *runtimeInstance) error {
	view := cloneInstance(runtime.view)
	directory := filepath.Join(manager.registry.directory, "queues")
	path := filepath.Join(directory, view.ID+".sqlite")
	if !validID(view.ID) || view.DatabasePath != path {
		return errors.New("control: refusing to purge a database outside its daemon-owned queue location")
	}
	// Never follow a replaced queue directory or recursively remove entries.
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("control: queue directory is unavailable or is not a directory: %s", directory)
	}
	paths := []string{path + "-wal", path + "-shm", path + "-journal", path}
	for _, filename := range paths {
		info, err := os.Lstat(filename)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("control: refusing to purge non-regular file %s", filename)
		}
	}
	// If deletion is interrupted, a retained registration must never restore
	// automatically against a partly removed database on the next daemon start.
	view.DesiredState = "stopped"
	if err := manager.registry.saveViews(ctx, []Instance{view}); err != nil {
		return err
	}
	runtime.view = view
	tx, err := manager.registry.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM instances WHERE id = ?", view.ID); err != nil {
		return err
	}
	for _, filename := range paths {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := os.Remove(filename); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("control: remove queue file: %w", err)
		}
	}
	return tx.Commit()
}
