package queue

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestRemoveJobCascadesHistoryAndProtectsActiveJobs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "queue.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	params := EnqueueParams{WatchName: "incoming", Path: "/input"}
	if _, _, err := store.Enqueue(ctx, params); err != nil {
		t.Fatal(err)
	}
	job, err := store.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveJob(ctx, job.ID); !errors.Is(err, ErrJobActive) {
		t.Fatalf("removed running job: %v", err)
	}
	if err := store.MarkPending(ctx, job.ID, job.RunID); err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveJob(ctx, job.ID); !errors.Is(err, ErrJobActive) {
		t.Fatalf("removed pending job: %v", err)
	}
	if err := store.InterruptResourceWait(ctx, job.ID, job.RunID, "canceled"); err != nil {
		t.Fatal(err)
	}
	job, err = store.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	command, err := store.StartCommand(ctx, CommandStart{RunID: job.RunID, Name: "test", Program: "unused"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteCommand(ctx, command, CommandResult{Status: CommandSucceeded, Stdout: "history"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Succeed(ctx, job.ID, job.RunID); err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveJob(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"jobs", "runs", "command_executions", "resource_wait_interruptions"} {
		var count int
		if err := store.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s still contains %d rows: %v", table, count, err)
		}
	}
	if err := store.RemoveJob(ctx, job.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing removal: %v", err)
	}
	added, created, err := store.Enqueue(ctx, params)
	if err != nil || !created || added.ID <= job.ID {
		t.Fatalf("removed fingerprint not reusable: %+v, %t, %v", added, created, err)
	}
	if err := store.RemoveJob(ctx, added.ID); err != nil {
		t.Fatalf("remove queued: %v", err)
	}
}
