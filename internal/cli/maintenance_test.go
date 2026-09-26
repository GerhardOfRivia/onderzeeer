package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GerhardOfRivia/onderzeeer/internal/queue"
)

func TestLocalMaintenanceCLI(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path, database := filepath.Join(root, "worker.yaml"), filepath.Join(root, "queue.db")
	contents := fmt.Sprintf("database: {path: %q, warn_size_bytes: 1, warn_free_percent: 0}\nwatches: [{name: input, path: %q, pipeline: [{name: step, program: unused}]}]\n", database, root)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	store, err := queue.Open(database, queue.WithClock(func() time.Time { return time.Now().Add(-48 * time.Hour) }))
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for i := 0; i < 2; i++ {
		if _, _, err := store.Enqueue(ctx, queue.EnqueueParams{WatchName: "input", Path: fmt.Sprint("/input/", i)}); err != nil {
			t.Fatal(err)
		}
		job, err := store.Claim(ctx)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, job.ID)
		command, err := store.StartCommand(ctx, queue.CommandStart{RunID: job.RunID, Program: "unused"})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.CompleteCommand(ctx, command, queue.CommandResult{Status: queue.CommandSucceeded, Stdout: "saved", Stderr: "error"}); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			err = store.Succeed(ctx, job.ID, job.RunID)
		} else {
			_, err = store.Fail(ctx, job.ID, job.RunID, "failed", 0)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	store.Close()
	code, stdout, stderr := managedCLI(t, "status", path, "--local")
	if code != 0 || !strings.Contains(stdout, "REUSABLE BYTES") || !strings.Contains(stdout, "WAL BYTES") || !strings.Contains(stdout, "DISK AVAILABLE BYTES") || !strings.Contains(stderr, "WARNING:") {
		t.Fatalf("storage status: %d, %s, %s", code, stdout, stderr)
	}
	code, stdout, stderr = managedCLI(t, "prune", path, "--local", "--older-than", "1d", "--dry-run")
	if code != 0 || stderr != "" || !strings.Contains(stdout, "Would prune captured output from 1 command(s): 10 bytes") {
		t.Fatalf("preview: %d, %s, %s", code, stdout, stderr)
	}
	code, stdout, stderr = managedCLI(t, "logs", path, fmt.Sprint(ids[0]), "--local")
	if code != 0 || !strings.Contains(stdout, "saved") {
		t.Fatalf("dry run changed output: %d, %s, %s", code, stdout, stderr)
	}
	code, stdout, stderr = managedCLI(t, "prune", path, "--older-than", "24h", "--local")
	if code != 0 || stderr != "" || !strings.Contains(stdout, "Pruned captured output from 1 command(s)") {
		t.Fatalf("prune: %d, %s, %s", code, stdout, stderr)
	}
	code, stdout, stderr = managedCLI(t, "logs", path, fmt.Sprint(ids[0]), "--local")
	if code != 0 || !strings.Contains(stdout, queue.PrunedOutputMarker) {
		t.Fatalf("logs missing pruning marker: %d, %s, %s", code, stdout, stderr)
	}
	code, stdout, stderr = managedCLI(t, "logs", path, fmt.Sprint(ids[1]), "--local")
	if code != 0 || !strings.Contains(stdout, "saved") {
		t.Fatalf("removed failed job output: %d, %s, %s", code, stdout, stderr)
	}
	code, stdout, stderr = managedCLI(t, "prune", path, "--older-than", "1d", "--include-failed", "--local")
	if code != 0 || !strings.Contains(stdout, "Pruned captured output from 1 command(s)") {
		t.Fatalf("include failed: %d, %s, %s", code, stdout, stderr)
	}
	code, stdout, stderr = managedCLI(t, "compact", path, "--local")
	if code != 0 || stderr != "" || !strings.Contains(stdout, "Compacted database") {
		t.Fatalf("compact: %d, %s, %s", code, stdout, stderr)
	}
	if err := os.Remove(database); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"prune", path, "--local", "--older-than", "1d"}, {"compact", path, "--local"}} {
		code, _, _ := managedCLI(t, args...)
		if code != 1 {
			t.Fatalf("missing database accepted: %v", args)
		}
		if _, err := os.Stat(database); !os.IsNotExist(err) {
			t.Fatalf("database recreated: %v", err)
		}
	}
}

func TestMaintenanceCLIRejectsInvalidArguments(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"prune"}, {"prune", "worker"}, {"prune", "worker", "--older-than", "0d"},
		{"prune", "worker", "--older-than", "-1h"}, {"prune", "worker", "--older-than", "999999999999999999999d"},
		{"prune", "worker", "--older-than", "1d", "--timeout", "0s"}, {"compact"}, {"compact", "a", "b"},
	} {
		code, stdout, _ := managedCLI(t, args...)
		if code != 2 || stdout != "" {
			t.Fatalf("accepted invalid args: %v: %d %s", args, code, stdout)
		}
	}
}
