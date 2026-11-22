package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/GerhardOfRivia/onderzeeer/internal/control"
	"github.com/GerhardOfRivia/onderzeeer/internal/testutil"
)

func TestDaemonStartsWithoutInstances(t *testing.T) {
	root := t.TempDir()
	configPath := writePlaceholderRunConfigAt(t, root, "worker.yaml")
	t.Setenv("ONDERZEEER_CONFIG", configPath)
	socket := filepath.Join(testutil.SocketDir(t), "control", "daemon.sock")
	process := launchTestDaemon(t, root, socket)
	client := control.NewClient(socket)
	defer client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	instances, err := client.List(ctx, true)
	if err != nil || len(instances) != 0 {
		t.Fatalf("new daemon unexpectedly registered configs: %+v, %v", instances, err)
	}
	process.stop(t, syscall.SIGTERM)
	if !strings.Contains(process.logs.String(), "restored_instances=0") {
		t.Fatalf("new daemon did not report empty registry: %s", process.logs.String())
	}
	if strings.Contains(process.logs.String(), "metrics listening") {
		t.Fatalf("metrics endpoint started without a listen address: %s", process.logs.String())
	}
	if _, err := os.Stat(socket); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("control socket remains after shutdown: %v", err)
	}
}

func TestDaemonRejectsConfigArguments(t *testing.T) {
	root := t.TempDir()
	configPath := writePlaceholderRunConfigAt(t, root, "worker.yaml")
	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "config", args: []string{configPath}},
		{name: "named config", args: []string{configPath, "worker"}},
		{name: "directory", args: []string{root}},
		{name: "missing path", args: []string{filepath.Join(root, "missing.yaml")}},
		{name: "empty argument", args: []string{""}},
		{name: "blank argument", args: []string{" \t"}},
		{name: "after terminator", args: []string{"--", root}},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtimeRoot := t.TempDir()
			state := filepath.Join(runtimeRoot, "state")
			socket := filepath.Join(runtimeRoot, "control", "daemon.sock")
			args := append([]string{"--state-dir", state, "--socket", socket}, test.args...)
			var stdout, stderr bytes.Buffer
			code := RunDaemon(args, &stdout, &stderr)
			want := "onderzeeerd: does not accept positional arguments; register instances with onderzeeer start <config> [name]\n"
			if code != 2 || stdout.Len() != 0 || stderr.String() != want {
				t.Fatalf("RunDaemon(%v) = %d, stdout %q, stderr %q; want usage error %q", args, code, stdout.String(), stderr.String(), want)
			}
			for _, path := range []string{state, filepath.Dir(socket)} {
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("invalid invocation created %s: %v", path, err)
				}
			}
		})
	}
}

func TestDaemonHelperProcess(t *testing.T) {
	if os.Getenv("ONDERZEEER_DAEMON_HELPER") != "1" {
		return
	}
	for index, arg := range os.Args {
		if arg == "--" {
			os.Exit(RunDaemon(os.Args[index+1:], os.Stdout, os.Stderr))
		}
	}
	os.Exit(2)
}

func TestWebPublicReadEnvironment(t *testing.T) {
	for _, test := range []struct {
		value string
		want  bool
	}{
		{"", false}, {" \t", false}, {"false", false}, {"0", false},
		{"true", true}, {"1", true}, {" TRUE ", true},
	} {
		t.Run(test.value, func(t *testing.T) {
			t.Setenv("ONDERZEEER_WEB_PUBLIC_READ", test.value)
			got, err := webPublicReadEnabled()
			if err != nil || got != test.want {
				t.Fatalf("webPublicReadEnabled() = %t, %v; want %t", got, err, test.want)
			}
		})
	}
	// Unset has the same private default as an empty value.
	t.Setenv("ONDERZEEER_WEB_PUBLIC_READ", "")
	if err := os.Unsetenv("ONDERZEEER_WEB_PUBLIC_READ"); err != nil {
		t.Fatal(err)
	}
	if got, err := webPublicReadEnabled(); err != nil || got {
		t.Fatalf("unset webPublicReadEnabled() = %t, %v", got, err)
	}
}

func TestDaemonRejectsInvalidPublicReadBeforeStartup(t *testing.T) {
	for _, value := range []string{"yes", "enabled", "tru"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("ONDERZEEER_WEB_PUBLIC_READ", value)
			root := t.TempDir()
			state := filepath.Join(root, "state")
			socket := filepath.Join(root, "control", "daemon.sock")
			var stdout, stderr bytes.Buffer
			code := RunDaemon([]string{"--state-dir", state, "--socket", socket}, &stdout, &stderr)
			if code != 2 || !strings.Contains(stderr.String(), "ONDERZEEER_WEB_PUBLIC_READ must be a boolean") {
				t.Fatalf("invalid public read: code=%d, stderr=%s", code, stderr.String())
			}
			for _, path := range []string{state, filepath.Dir(socket)} {
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("invalid environment created %s: %v", path, err)
				}
			}
		})
	}
}

func TestDaemonMetricsListenerIsSeparateFromTheDashboard(t *testing.T) {
	root := t.TempDir()
	socket := filepath.Join(testutil.SocketDir(t), "control", "metrics.sock")
	t.Setenv("ONDERZEEER_METRICS_LISTEN", "192.0.2.1:9090")
	process := launchTestDaemon(t, root, socket, "--web-listen", "127.0.0.1:0", "--metrics-listen", "127.0.0.1:0")
	client := control.NewClient(socket)
	defer client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	info, err := client.System(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.MetricsListen != "127.0.0.1:0" || info.MetricsAddress == "" || info.MetricsAddress == info.WebAddress || info.WebAddress == "" {
		t.Fatalf("metrics listen=%q address=%q web=%q", info.MetricsListen, info.MetricsAddress, info.WebAddress)
	}
	body := scrapeMetrics(t, info.MetricsAddress)
	for _, name := range []string{"onderzeeer_build_info", "onderzeeer_jobs_total", "go_goroutines", "process_resident_memory_bytes"} {
		if !strings.Contains(body, name) {
			t.Fatalf("scrape missing %s", name)
		}
	}
	if !strings.Contains(body, `version="dev"`) {
		t.Fatalf("build info missing version: %s", body)
	}
	webResponse, err := http.Get("http://" + info.WebAddress + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	webBody, err := io.ReadAll(webResponse.Body)
	webResponse.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(webBody), "onderzeeer_build_info") || strings.Contains(string(webBody), "go_goroutines") {
		t.Fatal("dashboard listener served Prometheus metrics")
	}
	missing, err := http.Get("http://" + info.MetricsAddress + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	missing.Body.Close()
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("metrics listener unknown path status = %d", missing.StatusCode)
	}
	code, stdout, stderr := managedCLI(t, "system", "--socket", socket)
	if code != 0 || stderr != "" || !strings.Contains(stdout, info.MetricsAddress) {
		t.Fatalf("system metrics report = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	process.stop(t, syscall.SIGTERM)
}

func TestDaemonMetricsFromEnvironment(t *testing.T) {
	root := t.TempDir()
	socket := filepath.Join(testutil.SocketDir(t), "control", "metrics-env.sock")
	t.Setenv("ONDERZEEER_METRICS_LISTEN", "127.0.0.1:0")
	process := launchTestDaemonArgs(t, socket, "--socket", socket, "--state-dir", filepath.Join(root, "state"), "--web-listen", "")
	client := control.NewClient(socket)
	defer client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	info, err := client.System(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.MetricsListen != "127.0.0.1:0" || info.MetricsAddress == "" {
		t.Fatalf("environment metrics = %+v", info)
	}
	body := scrapeMetrics(t, info.MetricsAddress)
	if !strings.Contains(body, "onderzeeer_build_info") {
		t.Fatalf("environment scrape missing build info: %s", body)
	}
	process.stop(t, syscall.SIGTERM)
}

func TestDaemonMetricsOffDisablesListener(t *testing.T) {
	root := t.TempDir()
	socket := filepath.Join(testutil.SocketDir(t), "control", "metrics-off.sock")
	t.Setenv("ONDERZEEER_METRICS_LISTEN", "off")
	process := launchTestDaemonArgs(t, socket, "--socket", socket, "--state-dir", filepath.Join(root, "state"), "--web-listen", "")
	client := control.NewClient(socket)
	defer client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	info, err := client.System(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.MetricsAddress != "" {
		t.Fatalf("disabled metrics address = %q", info.MetricsAddress)
	}
	process.stop(t, syscall.SIGTERM)
	if strings.Contains(process.logs.String(), "metrics listening") {
		t.Fatalf("disabled metrics listener started: %s", process.logs.String())
	}
}

func TestDaemonRejectsPublicMetricsAddressBeforeStartup(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	socket := filepath.Join(root, "control", "daemon.sock")
	var stdout, stderr bytes.Buffer
	code := RunDaemon([]string{"--state-dir", state, "--socket", socket, "--metrics-listen", "10.1.2.3:9090"}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "metrics: refusing") {
		t.Fatalf("public metrics address: code=%d stderr=%s", code, stderr.String())
	}
	for _, path := range []string{state, filepath.Dir(socket)} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("invalid metrics address created %s: %v", path, err)
		}
	}
}

func TestForegroundMetricsRejectsPublicAddress(t *testing.T) {
	configPath := writePlaceholderRunConfig(t, "worker.yaml")
	code, stdout, stderr := managedCLI(t, "test", "--metrics-listen", "192.0.2.1:9090", configPath)
	if code != 1 || stdout != "" || !strings.Contains(stderr, "metrics: refusing") {
		t.Fatalf("test metrics = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

func scrapeMetrics(t *testing.T, address string) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		response, err := http.Get("http://" + address + "/metrics")
		if err != nil {
			last = err
			time.Sleep(10 * time.Millisecond)
			continue
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusOK {
			t.Fatalf("metrics status %d body %s", response.StatusCode, body)
		}
		return string(body)
	}
	t.Fatal(last)
	return ""
}
