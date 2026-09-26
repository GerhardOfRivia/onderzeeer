package queue

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestClaimUsesCreationTimeAndIDAmongEligibleJobs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC)
	store, err := Open(filepath.Join(t.TempDir(), "queue.db"), WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	add := func(path string, available time.Time) Job {
		t.Helper()
		job, _, err := store.Enqueue(ctx, EnqueueParams{WatchName: "incoming", Path: path, AvailableAt: available, MaxRetries: 1})
		if err != nil {
			t.Fatal(err)
		}
		return job
	}
	future := add("/future", now.Add(time.Hour))
	oldest := add("/oldest", now)
	now = now.Add(time.Second)
	// Earlier availability must not let a newer job jump ahead.
	newer := add("/newer", now.Add(-time.Hour))
	tied := add("/same-time", now.Add(-2*time.Hour))
	for _, want := range []Job{oldest, newer, tied} {
		job, err := store.Claim(ctx)
		if err != nil || job.ID != want.ID {
			t.Fatalf("claim = %+v, %v; want %d", job, err, want.ID)
		}
		if job.ID == oldest.ID {
			if _, err := store.Fail(ctx, job.ID, job.RunID, "retry", time.Minute); err != nil {
				t.Fatal(err)
			}
		} else if err := store.Succeed(ctx, job.ID, job.RunID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Claim(ctx); !errors.Is(err, ErrNoJob) {
		t.Fatalf("claimed before retry delay: %v", err)
	}
	now = now.Add(2 * time.Minute)
	add("/latest", now.Add(-time.Hour))
	retry, err := store.Claim(ctx)
	if err != nil || retry.ID != oldest.ID {
		t.Fatalf("eligible retry lost original place: %+v, %v", retry, err)
	}
	now = now.Add(time.Hour)
	job, err := store.Claim(ctx)
	if err != nil || job.ID != future.ID {
		t.Fatalf("delayed oldest claim: %+v, %v", job, err)
	}
}
