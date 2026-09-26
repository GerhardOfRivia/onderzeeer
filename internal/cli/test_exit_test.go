package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GerhardOfRivia/onderzeeer/internal/queue"
)

func TestForegroundPipelineFailureExitsNonzero(t *testing.T) {
	for _, retries := range []int{0, 2} {
		t.Run(fmt.Sprint("retries=", retries), func(t *testing.T) {
			root := t.TempDir()
			incoming := filepath.Join(root, "incoming")
			if err := os.Mkdir(incoming, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(incoming, "input"), []byte("data"), 0o600); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "test.yaml")
			database := filepath.Join(root, "queue.db")
			config := fmt.Sprintf(`database: {path: %q}
queue: {workers: 2, max_retries: %d, retry_delay: 1h}
watches:
  - name: incoming
    path: %q
    process_existing: true
    settle_for: 0s
    pipeline: [{name: fail, program: /bin/sh, args: ['-c', 'echo failure >&2; exit 7']}]
`, database, retries, incoming)
			if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
				t.Fatal(err)
			}
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, executable, "-test.run=^TestCLIHelperProcess$", "--", "test", path)
			command.Env = append(os.Environ(), "ONDERZEEER_CLI_HELPER=1")
			output, err := command.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatalf("test kept watching after failure: %s", output)
			}
			if err == nil || command.ProcessState.ExitCode() != 1 || !strings.Contains(string(output), "failure") {
				t.Fatalf("exit = %v, %v; output = %s", command.ProcessState, err, output)
			}
			store, err := queue.OpenReadOnly(database)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			jobs, err := store.ListJobs(context.Background(), queue.JobFilter{})
			want := queue.StatusFailed
			if retries > 0 {
				want = queue.StatusQueued
			}
			if err != nil || len(jobs) != 1 || jobs[0].Status != want || jobs[0].Attempts != 1 {
				t.Fatalf("failure was not persisted: %+v, %v", jobs, err)
			}
		})
	}
}

func TestCLIHelperProcess(t *testing.T) {
	if os.Getenv("ONDERZEEER_CLI_HELPER") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Exit(Run(os.Args[i+1:], os.Stdout, os.Stderr))
		}
	}
	os.Exit(2)
}
