package daemon

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GerhardOfRivia/onderzeeer/internal/config"
	"github.com/GerhardOfRivia/onderzeeer/internal/queue"
	"github.com/GerhardOfRivia/onderzeeer/internal/watcher"
)

func TestFileExternalIDHookIsStoredOnEnqueue(t *testing.T) {
	SetFileExternalID(func(_ config.WatchConfig, file watcher.File) (string, error) {
		return " drop-" + filepath.Base(file.Path) + " ", nil
	})
	t.Cleanup(func() { SetFileExternalID(nil) })

	directory := t.TempDir()
	incoming := filepath.Join(directory, "incoming")
	if err := os.Mkdir(incoming, 0o700); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(incoming, "batch.csv")
	if err := os.WriteFile(input, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(directory, "queue.db")
	cfg := &config.Config{
		Queue:    config.QueueConfig{Workers: 1, RetryDelay: config.Duration{Duration: time.Millisecond}},
		Database: config.DatabaseConfig{Path: database},
		Watches: []config.WatchConfig{{
			Name: "incoming", Path: incoming, ProcessExisting: true,
			SettleFor: config.Duration{Duration: 10 * time.Millisecond},
			Pipeline: []config.CommandConfig{{
				Name: "record", Program: os.Args[0],
				Args: []string{"-test.run=^TestDaemonHelperProcess$", "--", "{{file}}", "{{job_id}}"},
				Env:  map[string]string{"ONDERZEEER_DAEMON_TEST_MARKER": filepath.Join(directory, "executed")},
			}},
		}},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil))) }()

	// Read only. A writable Open races the daemon for the new database and, on
	// a busy CI runner, makes the daemon's own Open fail with SQLITE_BUSY
	// before it can enqueue the file.
	deadline := time.Now().Add(10 * time.Second)
	var jobs []queue.Job
	for {
		select {
		case err := <-done:
			cancel()
			t.Fatalf("daemon exited before the file was queued: %v", err)
		default:
		}
		store, err := queue.OpenReadOnly(database)
		if err == nil {
			jobs, err = store.ListJobs(context.Background(), queue.JobFilter{ExternalID: "drop-batch.csv"})
			closeErr := store.Close()
			if err == nil && closeErr == nil && len(jobs) == 1 && jobs[0].ExternalID == "drop-batch.csv" && jobs[0].Path == input {
				cancel()
				select {
				case err := <-done:
					if err != nil && !errors.Is(err, context.Canceled) {
						t.Fatalf("daemon: %v", err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("daemon did not stop")
				}
				return
			}
		}
		if time.Now().After(deadline) {
			cancel()
			var runErr error
			select {
			case runErr = <-done:
			case <-time.After(time.Second):
			}
			t.Fatalf("hook id was not stored: %+v; daemon: %v", jobs, runErr)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestExternalIDHookEmptyAndError(t *testing.T) {
	if id, err := externalIDFor(config.WatchConfig{}, watcher.File{Path: "/drop/a"}); err != nil || id != "" {
		t.Fatalf("nil hook = %q %v", id, err)
	}
	SetFileExternalID(func(config.WatchConfig, watcher.File) (string, error) {
		return "  spaced  ", nil
	})
	id, err := externalIDFor(config.WatchConfig{}, watcher.File{})
	SetFileExternalID(func(config.WatchConfig, watcher.File) (string, error) {
		return "", errors.New("sidecar missing")
	})
	_, hookErr := externalIDFor(config.WatchConfig{}, watcher.File{})
	SetFileExternalID(nil)
	if err != nil || id != "spaced" {
		t.Fatalf("trimmed hook = %q %v", id, err)
	}
	if hookErr == nil || hookErr.Error() != "sidecar missing" {
		t.Fatalf("hook error = %v", hookErr)
	}
}
