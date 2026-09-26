package control

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/GerhardOfRivia/onderzeeer/internal/config"
	"github.com/GerhardOfRivia/onderzeeer/internal/queue"
)

func TestQueueReaderRemovesOnlyInactiveJobs(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path, database := filepath.Join(root, "worker.yaml"), filepath.Join(root, "queue.db")
	_, client, socket := newUnixTransportHarness(t, Options{
		Loader: mappedLoader(t, map[string]*config.Config{path: testConfig(database)}),
		Runner: waitingRunner,
	})
	ctx := context.Background()
	instances, err := client.Start(ctx, []string{path}, "worker")
	if err != nil {
		t.Fatal(err)
	}
	store, err := queue.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, _, err := store.Enqueue(ctx, queue.EnqueueParams{WatchName: "incoming", Path: "/input"}); err != nil {
		t.Fatal(err)
	}
	job, err := store.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	reader := NewQueueReader(socket, instances[0].ID)
	defer reader.Close()
	for _, search := range []string{"INPUT", "does-not-match"} {
		jobs, err := reader.ListJobs(ctx, queue.JobFilter{Search: search, WatchName: "incoming", Limit: 10})
		want := 1
		if search == "does-not-match" {
			want = 0
		}
		if err != nil || len(jobs) != want {
			t.Fatalf("remote search %q = %+v, %v", search, jobs, err)
		}
	}
	if err := reader.RemoveJob(ctx, job.ID); !errors.Is(err, queue.ErrJobActive) {
		t.Fatalf("removed active job: %v", err)
	}
	if err := store.MarkPending(ctx, job.ID, job.RunID); err != nil {
		t.Fatal(err)
	}
	if err := reader.RemoveJob(ctx, job.ID); !errors.Is(err, queue.ErrJobActive) {
		t.Fatalf("removed pending job: %v", err)
	}
	if err := store.InterruptResourceWait(ctx, job.ID, job.RunID, "canceled"); err != nil {
		t.Fatal(err)
	}
	if err := reader.RemoveJob(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.GetJob(ctx, job.ID); !errors.Is(err, queue.ErrNotFound) {
		t.Fatalf("removed job still readable: %v", err)
	}
	if err := reader.RemoveJob(ctx, job.ID); !errors.Is(err, queue.ErrNotFound) {
		t.Fatalf("missing removal: %v", err)
	}
	counts, err := reader.Counts(ctx)
	if err != nil || counts.Total != 0 {
		t.Fatalf("counts after removal: %+v, %v", counts, err)
	}
}
