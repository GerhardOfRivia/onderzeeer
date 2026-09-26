package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/GerhardOfRivia/onderzeeer/internal/control"
	"github.com/GerhardOfRivia/onderzeeer/internal/testutil"
)

func TestSystemReportsDaemonSettingsAndPurges(t *testing.T) {
	root := t.TempDir()
	socket := filepath.Join(testutil.SocketDir(t), "control", "daemon.sock")
	t.Setenv("ONDERZEEER_WEB_PUBLIC_READ", "true")
	process := launchTestDaemon(t, root, socket, "--web-listen", "127.0.0.1:0", "--log-level", "debug")
	client := control.NewClient(socket)
	defer client.CloseIdleConnections()
	ctx := context.Background()
	info, err := client.System(ctx)
	if err != nil || info.Version != "dev" || info.LogLevel != "debug" || info.WebListen != "127.0.0.1:0" || info.WebAddress == "" || info.WebAddress == info.WebListen || !info.WebPublicRead || info.PID != process.command.Process.Pid || info.WebTokenPath != socket+".web-token" {
		t.Fatalf("daemon system = %+v, %v", info, err)
	}
	// Report the daemon's settings even when the client's environment differs.
	t.Setenv("ONDERZEEER_STATE_DIR", filepath.Join(root, "wrong-state"))
	t.Setenv("ONDERZEEER_WEB_PUBLIC_READ", "false")
	code, stdout, stderr := managedCLI(t, "system", "--socket", socket)
	if code != 0 || stderr != "" {
		t.Fatalf("system = %d, %q, %q", code, stdout, stderr)
	}
	for _, want := range []string{"debug", "true", strconv.Itoa(info.PID), info.StateDirectory, info.RegistryPath, info.QueueDirectory, info.SocketLockPath, info.StateLockPath, info.WebAddress, info.WebTokenPath} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("system did not report %q: %s", want, stdout)
		}
	}
	if token, err := os.ReadFile(info.WebTokenPath); err != nil || strings.Contains(stdout, strings.TrimSpace(string(token))) {
		t.Fatalf("token read failed or token leaked: %v", err)
	}
	watch := filepath.Join(root, "input")
	if err := os.Mkdir(watch, 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(watch, "source.txt")
	if err := os.WriteFile(source, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "worker.yaml")
	contents := fmt.Sprintf("queue: {workers: 1}\nwatches:\n  - name: input\n    path: %q\n    process_existing: true\n    settle_for: 0s\n    pipeline:\n      - name: inspect\n        executor: shell\n        command: 'echo captured'\n", watch)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	instances, err := client.Start(ctx, []string{path}, "worker")
	if err != nil {
		t.Fatal(err)
	}
	waitSucceeded(t, socket, instances[0].ID, 1)
	code, stdout, stderr = managedCLI(t, "system", "--socket", socket, "--purge")
	if code != 0 || stderr != "" || !strings.Contains(stdout, "No inactive queues to purge.") || strings.Contains(stdout, "[y/N]") {
		t.Fatalf("active purge = %d, %q, %q", code, stdout, stderr)
	}
	if _, err := client.Stop(ctx, "worker"); err != nil {
		t.Fatal(err)
	}
	for _, answer := range []string{"", "\n", "no\n", "maybe\n", "yes"} {
		code, stdout, stderr := systemCLIWithInput(t, answer, "system", "--purge", "--socket", socket)
		if code != 0 || stderr != "" || !strings.Contains(stdout, "Purge canceled. No queues were removed.") {
			t.Fatalf("purge answer %q = %d, %q, %q", answer, code, stdout, stderr)
		}
		for _, want := range []string{instances[0].ID, "worker", "exited", instances[0].DatabasePath, "execution history", "captured output", "cannot be undone", "[y/N]"} {
			if !strings.Contains(stdout, want) {
				t.Fatalf("purge preview omitted %q: %s", want, stdout)
			}
		}
		if _, err := os.Stat(instances[0].DatabasePath); err != nil {
			t.Fatal("declined purge removed database", err)
		}
		if _, err := client.Get(ctx, "worker"); err != nil {
			t.Fatal("declined purge removed registration", err)
		}
	}
	blocker := instances[0].DatabasePath + "-wal"
	if err := os.Mkdir(blocker, 0o700); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = systemCLIWithInput(t, "y\n", "system", "--purge", "--socket", socket)
	if code != 1 || !strings.Contains(stderr, "purge worker") || !strings.Contains(stdout, "Purged 0 inactive queue(s)") {
		t.Fatalf("failed purge = %d, %q, %q", code, stdout, stderr)
	}
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	// Exercise the real CLI entry point to verify that it consumes stdin.
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	commandCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(commandCtx, executable, "-test.run=^TestCLIHelperProcess$", "--", "system", "--purge", "--socket", socket)
	command.Env = append(os.Environ(), "ONDERZEEER_CLI_HELPER=1")
	command.Stdin = strings.NewReader("yes\n")
	output, err := command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "Purged 1 inactive queue(s)") || !strings.Contains(string(output), instances[0].DatabasePath) {
		t.Fatalf("confirmed purge = %v, %s", err, output)
	}
	if _, err := os.Stat(instances[0].DatabasePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("database was not removed: %v", err)
	}
	for _, kept := range []string{path, source} {
		if _, err := os.Stat(kept); err != nil {
			t.Fatalf("purge removed %s: %v", kept, err)
		}
	}
	process.stop(t, syscall.SIGTERM)
}

func systemCLIWithInput(t *testing.T, input string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := runVersionWithInput(args, strings.NewReader(input), &stdout, &stderr, "dev")
	return code, stdout.String(), stderr.String()
}

func TestPurgeConfirmationRequiresExplicitAgreement(t *testing.T) {
	for _, answer := range []string{"y\n", "Y\n", "yes\n", " YES \r\n", "\n", "n\n", "no\n", "maybe\n", "", "yes"} {
		t.Run(strconv.Quote(answer), func(t *testing.T) {
			var output bytes.Buffer
			got, err := confirmSystemPurge(strings.NewReader(answer), &output, []control.Instance{{ID: "abc123def456", Name: "worker", DatabasePath: "/state/queues/worker.sqlite"}})
			normalized := strings.ToLower(strings.TrimSpace(answer))
			want := strings.HasSuffix(answer, "\n") && (normalized == "y" || normalized == "yes")
			if err != nil || got != want {
				t.Fatalf("confirmation = %t, %v; want %t", got, err, want)
			}
		})
	}
}

func TestSystemUsageAndUnavailableDaemon(t *testing.T) {
	code, _, stderr := managedCLI(t, "system", "unexpected")
	if code != 2 || !strings.Contains(stderr, "does not accept positional arguments") {
		t.Fatalf("system usage = %d, %s", code, stderr)
	}
	for _, purge := range []bool{false, true} {
		code, stdout, stderr := managedCLI(t, "system", "--socket", filepath.Join(t.TempDir(), "missing.sock"), "--purge="+strconv.FormatBool(purge))
		if code != 1 || stdout != "" || !strings.Contains(stderr, "daemon is unavailable") {
			t.Fatalf("unavailable system = %d, %q, %q", code, stdout, stderr)
		}
	}
}
