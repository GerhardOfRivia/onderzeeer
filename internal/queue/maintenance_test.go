package queue

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func maintenanceStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "queue.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// Seed prior command attempts independently from their job's current state.
func seedMaintenanceJob(t *testing.T, store *Store, status Status, finished time.Time, commands int, output string) (int64, int64) {
	t.Helper()
	stamp := unixNano(finished)
	result, err := store.db.Exec(`INSERT INTO jobs (watch_name,path,fingerprint,status,attempts,max_retries,available_at,created_at,updated_at,finished_at)
VALUES ('incoming', ?, 'v1', ?, 1, 0, ?, ?, ?, ?)`, fmt.Sprintf("/input/%s/%d/%d", status, stamp, commands), status, stamp, stamp, stamp, stamp)
	if err != nil {
		t.Fatal(err)
	}
	job, _ := result.LastInsertId()
	result, err = store.db.Exec("INSERT INTO runs (job_id,attempt,status,error,started_at,finished_at) VALUES (?,1,'FAILED','original error',?,?)", job, stamp, stamp)
	if err != nil {
		t.Fatal(err)
	}
	run, _ := result.LastInsertId()
	for i := 0; i < commands; i++ {
		if _, err := store.db.Exec(`INSERT INTO command_executions (run_id,sequence,name,program,args_json,env_json,working_dir,timeout_ns,status,stdout,stderr,error,exit_code,started_at,finished_at)
VALUES (?,?,'step','unused','[]','[]','/',0,'FAILED',?,'stderr','original error',7,?,?)`, run, i, output, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	return job, run
}

func TestPrunePreservesActiveRecentFailedJobsAndDeduplication(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := maintenanceStore(t)
	cutoff := time.Now().Add(-24 * time.Hour)
	old := cutoff.Add(-time.Hour)
	job, run := seedMaintenanceJob(t, store, StatusSucceeded, old, pruneBatchSize+3, "héllo\x00世界")
	var protected []int64
	for _, status := range []Status{StatusFailed, StatusQueued, StatusPending, StatusRunning} {
		_, id := seedMaintenanceJob(t, store, status, old, 1, "keep")
		protected = append(protected, id)
	}
	for _, finished := range []time.Time{cutoff, cutoff.Add(time.Hour)} {
		_, id := seedMaintenanceJob(t, store, StatusSucceeded, finished, 1, "keep")
		protected = append(protected, id)
	}
	reader, err := OpenReadOnly(storePath(t, store))
	if err != nil {
		t.Fatal(err)
	}
	preview, err := reader.PruneOutput(ctx, PruneOptions{Before: cutoff, DryRun: true})
	reader.Close()
	wantBytes := int64((pruneBatchSize + 3) * (len("héllo\x00世界") + len("stderr")))
	if err != nil || !preview.DryRun || preview.Commands != pruneBatchSize+3 || preview.OutputBytes != wantBytes {
		t.Fatalf("preview: %+v, %v", preview, err)
	}
	commands, err := store.ListCommands(ctx, run)
	if err != nil || commands[0].Stdout != "héllo\x00世界" {
		t.Fatalf("dry run modified output: %+v, %v", commands, err)
	}
	result, err := store.PruneOutput(ctx, PruneOptions{Before: cutoff})
	if err != nil || result.Commands != preview.Commands || result.OutputBytes != preview.OutputBytes {
		t.Fatalf("prune: %+v, %v", result, err)
	}
	commands, err = store.ListCommands(ctx, run)
	if err != nil || len(commands) != pruneBatchSize+3 {
		t.Fatalf("commands: %+v, %v", commands, err)
	}
	for _, command := range commands {
		if command.Stdout != PrunedOutputMarker || command.Stderr != "" || command.Error != "original error" || command.ExitCode == nil || *command.ExitCode != 7 {
			t.Fatalf("incorrect pruned command: %+v", command)
		}
	}
	for _, id := range protected {
		commands, err := store.ListCommands(ctx, id)
		if err != nil || commands[0].Stdout != "keep" {
			t.Fatalf("protected run %d: %+v, %v", id, commands, err)
		}
	}
	existing, err := store.GetJob(ctx, job)
	if err != nil {
		t.Fatal(err)
	}
	again, created, err := store.Enqueue(ctx, EnqueueParams{WatchName: existing.WatchName, Path: existing.Path, Fingerprint: existing.Fingerprint})
	if err != nil || created || again.ID != job {
		t.Fatalf("deduplication lost: %+v, %t, %v", again, created, err)
	}
	result, err = store.PruneOutput(ctx, PruneOptions{Before: cutoff})
	if err != nil || result.Commands != 0 || result.OutputBytes != 0 {
		t.Fatalf("repeat prune: %+v, %v", result, err)
	}
	result, err = store.PruneOutput(ctx, PruneOptions{Before: cutoff, IncludeFailed: true})
	if err != nil || result.Commands != 1 {
		t.Fatalf("include failed: %+v, %v", result, err)
	}
}

func storePath(t *testing.T, store *Store) string {
	t.Helper()
	var sequence int
	var name, path string
	if err := store.db.QueryRow("PRAGMA database_list").Scan(&sequence, &name, &path); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPruneReportsCommittedBatchesAfterFailure(t *testing.T) {
	t.Parallel()
	store := maintenanceStore(t)
	_, run := seedMaintenanceJob(t, store, StatusSucceeded, time.Now().Add(-48*time.Hour), pruneBatchSize+2, "saved")
	if _, err := store.db.Exec(fmt.Sprintf(`CREATE TRIGGER fail_prune BEFORE UPDATE ON command_executions WHEN OLD.sequence = %d BEGIN SELECT RAISE(ABORT, 'test failure'); END`, pruneBatchSize+1)); err != nil {
		t.Fatal(err)
	}
	options := PruneOptions{Before: time.Now().Add(-time.Hour)}
	result, err := store.PruneOutput(context.Background(), options)
	if err == nil || result.Commands != pruneBatchSize {
		t.Fatalf("partial progress: %+v, %v", result, err)
	}
	commands, err := store.ListCommands(context.Background(), run)
	if err != nil || commands[pruneBatchSize].Stdout != "saved" {
		t.Fatalf("failed batch not rolled back: %+v, %v", commands, err)
	}
	if _, err := store.db.Exec("DROP TRIGGER fail_prune"); err != nil {
		t.Fatal(err)
	}
	result, err = store.PruneOutput(context.Background(), options)
	if err != nil || result.Commands != 2 {
		t.Fatalf("retry: %+v, %v", result, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.PruneOutput(ctx, options); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled prune: %v", err)
	}
	for _, cutoff := range []time.Time{{}, time.Now().Add(time.Hour), time.Date(1000, 1, 1, 0, 0, 0, 0, time.UTC)} {
		if _, err := store.PruneOutput(context.Background(), PruneOptions{Before: cutoff}); err == nil {
			t.Fatal("accepted invalid cutoff")
		}
	}
}

func TestStoragePruningAndCompaction(t *testing.T) {
	t.Parallel()
	store := maintenanceStore(t)
	ctx := context.Background()
	_, run := seedMaintenanceJob(t, store, StatusSucceeded, time.Now().Add(-48*time.Hour), 3, strings.Repeat("x", 1<<20))
	if _, err := store.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	before, err := store.Storage(ctx)
	if err != nil || before.DatabaseBytes < 3<<20 || before.Disk == nil || before.Disk.TotalBytes == 0 {
		t.Fatalf("storage before: %+v, %v", before, err)
	}
	if _, err := store.PruneOutput(ctx, PruneOptions{Before: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	pruned, err := store.Storage(ctx)
	if err != nil || pruned.ReusableBytes < 2<<20 {
		t.Fatalf("reusable storage: %+v, %v", pruned, err)
	}
	if err := store.Compact(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := store.Storage(ctx)
	if err != nil || after.DatabaseBytes >= before.DatabaseBytes || after.WALBytes != 0 {
		t.Fatalf("compaction: %+v -> %+v, %v", before, after, err)
	}
	commands, err := store.ListCommands(ctx, run)
	if err != nil || commands[0].Stdout != PrunedOutputMarker {
		t.Fatalf("compaction damaged history: %+v, %v", commands, err)
	}
	seedMaintenanceJob(t, store, StatusPending, time.Now(), 1, "keep")
	if err := store.Compact(ctx); err == nil {
		t.Fatal("compacted active job")
	}
}

func TestStorageWarningThresholds(t *testing.T) {
	info := StorageInfo{DatabaseBytes: 80, WALBytes: 20, Disk: &DiskSpace{AvailableBytes: 10, TotalBytes: 100}}
	info.SetWarnings(100, 10)
	if len(info.Warnings) != 2 {
		t.Fatalf("threshold warnings: %+v", info)
	}
	info.SetWarnings(101, 9)
	if len(info.Warnings) != 0 {
		t.Fatal(info.Warnings)
	}
	info.SetWarnings(0, 0)
	if len(info.Warnings) != 0 {
		t.Fatal(info.Warnings)
	}
	info.Disk = nil
	info.SetWarnings(0, 10)
	if len(info.Warnings) != 0 {
		t.Fatal("unavailable disk treated as empty")
	}
}
