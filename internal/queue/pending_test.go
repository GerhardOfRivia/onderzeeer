package queue

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPendingTransitionsCountsAndHistory(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "queue.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, _, err := store.Enqueue(ctx, EnqueueParams{WatchName: "incoming", Path: "/input"}); err != nil {
		t.Fatal(err)
	}
	job, err := store.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	start := CommandStart{RunID: job.RunID, Name: "prepare", Program: "unused"}
	command, err := store.StartCommand(ctx, start)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkPending(ctx, job.ID, job.RunID); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("executing command became pending: %v", err)
	}
	if err := store.CompleteCommand(ctx, command, CommandResult{Status: CommandSucceeded}); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkPending(ctx, job.ID+1, job.RunID); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("mismatched job/run accepted: %v", err)
	}
	if err := store.MarkPending(ctx, job.ID, job.RunID); err != nil {
		t.Fatal(err)
	}
	counts, err := store.Counts(ctx)
	if err != nil || counts.Pending != 1 || counts.Running != 0 || counts.Total != 1 {
		t.Fatalf("pending counts: %+v, %v", counts, err)
	}
	jobs, err := store.ListJobs(ctx, JobFilter{Status: StatusPending})
	if err != nil || len(jobs) != 1 || jobs[0].ID != job.ID {
		t.Fatalf("pending filter: %+v, %v", jobs, err)
	}
	runs, err := store.ListRuns(ctx, job.ID)
	if err != nil || len(runs) != 1 || runs[0].Status != StatusPending {
		t.Fatalf("pending history: %+v, %v", runs, err)
	}
	if _, err := store.Claim(ctx); !errors.Is(err, ErrNoJob) {
		t.Fatalf("pending job reclaimed: %v", err)
	}
	start.Sequence++
	if _, err := store.StartCommand(ctx, start); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("pending run executed: %v", err)
	}
	if err := store.Succeed(ctx, job.ID, job.RunID); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("pending run succeeded: %v", err)
	}
	if err := store.MarkRunning(ctx, job.ID, job.RunID); err != nil {
		t.Fatal(err)
	}
	command, err = store.StartCommand(ctx, start)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteCommand(ctx, command, CommandResult{Status: CommandSucceeded}); err != nil {
		t.Fatal(err)
	}
	if err := store.Succeed(ctx, job.ID, job.RunID); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkPending(ctx, job.ID, job.RunID); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("terminal run became pending: %v", err)
	}
}

func TestRecoverPendingPreservesRetryBudget(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "queue.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Enqueue(ctx, EnqueueParams{WatchName: "incoming", Path: "/input", MaxRetries: 1}); err != nil {
		t.Fatal(err)
	}
	job, err := store.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkPending(ctx, job.ID, job.RunID); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if count, err := store.RecoverRunning(ctx); err != nil || count != 1 {
		t.Fatalf("recover pending = %d, %v", count, err)
	}
	if count, err := store.RecoverRunning(ctx); err != nil || count != 0 {
		t.Fatalf("repeat recovery = %d, %v", count, err)
	}
	runs, err := store.ListRuns(ctx, job.ID)
	if err != nil || len(runs) != 1 || runs[0].Status != StatusFailed || runs[0].FinishedAt == nil {
		t.Fatalf("recovered history: %+v, %v", runs, err)
	}
	for _, want := range []Status{StatusQueued, StatusFailed} {
		job, err = store.Claim(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if status, err := store.Fail(ctx, job.ID, job.RunID, "execution failed", 0); err != nil || status != want {
			t.Fatalf("retry status = %s, %v; want %s", status, err, want)
		}
	}
}

func TestPendingMigrationPreservesHistoryAndIDs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	legacy := strings.ReplaceAll(schema, "'PENDING', ", "")
	if _, err := db.Exec("PRAGMA foreign_keys = ON;" + legacy); err != nil {
		t.Fatal(err)
	}
	old := &Store{db: db, now: time.Now}
	if _, _, err := old.Enqueue(ctx, EnqueueParams{WatchName: "incoming", Path: "/done"}); err != nil {
		t.Fatal(err)
	}
	first, err := old.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	command, err := old.StartCommand(ctx, CommandStart{RunID: first.RunID, Name: "old", Program: "unused"})
	if err != nil {
		t.Fatal(err)
	}
	if err := old.CompleteCommand(ctx, command, CommandResult{Status: CommandSucceeded, Stdout: "preserved"}); err != nil {
		t.Fatal(err)
	}
	if err := old.Succeed(ctx, first.ID, first.RunID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := old.Enqueue(ctx, EnqueueParams{WatchName: "incoming", Path: "/active"}); err != nil {
		t.Fatal(err)
	}
	active, err := old.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO resource_wait_interruptions VALUES (?); UPDATE sqlite_sequence SET seq = 100 WHERE name IN ('jobs', 'runs');", first.RunID); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	commands, err := store.ListCommands(ctx, first.RunID)
	if err != nil || len(commands) != 1 || commands[0].ID != command || commands[0].Stdout != "preserved" {
		t.Fatalf("migrated commands: %+v, %v", commands, err)
	}
	var exemptions, foreignKeys int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM resource_wait_interruptions").Scan(&exemptions); err != nil || exemptions != 1 {
		t.Fatalf("lost retry exemption: %d, %v", exemptions, err)
	}
	if err := store.db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil || foreignKeys != 1 {
		t.Fatalf("foreign keys = %d, %v", foreignKeys, err)
	}
	if err := store.MarkPending(ctx, active.ID, active.RunID); err != nil {
		t.Fatal(err)
	}
	if status, err := store.Fail(ctx, active.ID, active.RunID, "resume persistence failed", 0); err != nil || status != StatusFailed {
		t.Fatalf("fail pending: %s, %v", status, err)
	}
	if _, _, err := store.Enqueue(ctx, EnqueueParams{WatchName: "incoming", Path: "/new"}); err != nil {
		t.Fatal(err)
	}
	newJob, err := store.Claim(ctx)
	if err != nil || newJob.ID != 101 || newJob.RunID != 101 {
		t.Fatalf("lost ID sequence: %+v, %v", newJob, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.MarkPending(ctx, newJob.ID, newJob.RunID); err != nil {
		t.Fatal(err)
	}
}
