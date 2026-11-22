package metrics

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestDisabledAddressesDoNotListen(t *testing.T) {
	for _, address := range []string{"", "  ", "off", "OFF", "disabled", "none", "-"} {
		server, err := Listen(address, nil)
		if err != nil || server != nil {
			t.Fatalf("Listen(%q) = (%v, %v), want a disabled endpoint", address, server, err)
		}
	}
	t.Setenv("ONDERZEEER_METRICS_LISTEN", "")
	if !Disabled(FromEnv()) {
		t.Fatalf("empty ONDERZEEER_METRICS_LISTEN = %q", FromEnv())
	}
	t.Setenv("ONDERZEEER_METRICS_LISTEN", " 127.0.0.1:9090 ")
	if FromEnv() != "127.0.0.1:9090" {
		t.Fatalf("FromEnv = %q", FromEnv())
	}
}

func TestListenRejectsPublicAndIncompleteAddresses(t *testing.T) {
	for _, address := range []string{"192.0.2.1:9090", "metrics.example:9090", "127.0.0.1", "10.0.0.8:9090"} {
		server, err := Listen(address, nil)
		if err == nil || server != nil {
			if server != nil {
				_ = server.Close()
			}
			t.Fatalf("address %s was accepted: %v", address, err)
		}
		if !strings.Contains(err.Error(), "metrics:") {
			t.Fatalf("address %s error = %v", address, err)
		}
	}
}

func TestListenLoopbackAndWildcard(t *testing.T) {
	var warnings strings.Builder
	logger := slog.New(slog.NewTextHandler(&warnings, &slog.HandlerOptions{Level: slog.LevelWarn}))
	loopback, err := Listen("127.0.0.1:0", logger)
	if err != nil {
		t.Fatal(err)
	}
	defer loopback.Close()
	if warnings.Len() != 0 {
		t.Fatalf("loopback listen warned: %s", warnings.String())
	}
	local, err := Listen("localhost:0", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	v6, err := Listen("[::1]:0", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer v6.Close()

	warnings.Reset()
	wildcard, err := Listen("0.0.0.0:0", logger)
	if err != nil {
		t.Fatal(err)
	}
	defer wildcard.Close()
	if !strings.Contains(warnings.String(), "all network interfaces") {
		t.Fatalf("wildcard warning = %q", warnings.String())
	}
}

func TestMetricsEndpointExposesApplicationAndRuntimeSeries(t *testing.T) {
	server, err := Listen("127.0.0.1:0", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- server.Serve(ctx)
	}()
	defer server.Close()

	SetVersion("test-metrics")
	secret := "/var/drop/secret.csv"
	beforeOther := Sample("onderzeeer_http_requests_total", map[string]string{"method": "other", "route": "other", "code": "2xx"})
	beforeJobs := Sample("onderzeeer_jobs_total", map[string]string{"outcome": "other"})
	beforeRecovered := Sample("onderzeeer_jobs_recovered_total", nil)
	ObserveHTTP("TRACE", secret, 200, 20*time.Millisecond)
	ObserveJob(secret, 1500*time.Millisecond)
	JobEnqueued(secret)
	File(secret)
	AddRecovered(0)
	AddRecovered(-3)
	AddRecovered(2)
	ObserveCommand("custom-runtime", "succeeded", 10*time.Millisecond)
	if Sample("onderzeeer_http_requests_total", map[string]string{"method": "other", "route": "other", "code": "2xx"}) != beforeOther+1 {
		t.Fatal("unexpected HTTP label was not collapsed")
	}
	if Sample("onderzeeer_jobs_total", map[string]string{"outcome": "other"}) != beforeJobs+1 {
		t.Fatal("unexpected job outcome was not collapsed")
	}
	if Sample("onderzeeer_job_duration_seconds", map[string]string{"outcome": "other"}) < 1 {
		t.Fatal("duration histogram did not record the collapsed series")
	}
	if Sample("onderzeeer_jobs_recovered_total", nil) != beforeRecovered+2 {
		t.Fatal("non-positive recovery counts were recorded")
	}
	if Sample("onderzeeer_commands_total", map[string]string{"executor": "other", "result": "succeeded"}) < 1 {
		t.Fatal("unknown executor was not collapsed")
	}

	body := scrape(t, server.Address())
	for _, name := range []string{
		"onderzeeer_http_requests_total",
		"onderzeeer_http_request_duration_seconds",
		"onderzeeer_files_total",
		"onderzeeer_files_pending",
		"onderzeeer_watcher_errors_total",
		"onderzeeer_jobs_enqueued_total",
		"onderzeeer_jobs_total",
		"onderzeeer_job_duration_seconds",
		"onderzeeer_jobs_active",
		"onderzeeer_jobs_waiting_resources",
		"onderzeeer_jobs_recovered_total",
		"onderzeeer_commands_total",
		"onderzeeer_command_duration_seconds",
		"onderzeeer_commands_active",
		"onderzeeer_resource_waits_total",
		"onderzeeer_resource_wait_duration_seconds",
		"onderzeeer_instances_total",
		"onderzeeer_instance_duration_seconds",
		"onderzeeer_instances_active",
		"onderzeeer_build_info",
		"go_goroutines",
		"go_memstats_alloc_bytes",
		"process_start_time_seconds",
		"process_resident_memory_bytes",
	} {
		if !strings.Contains(body, name) {
			t.Fatalf("scrape missing %s", name)
		}
	}
	if !strings.Contains(body, `version="test-metrics"`) {
		start := strings.Index(body, "onderzeeer_build_info")
		t.Fatalf("build info missing version: %s", body[start:])
	}
	if strings.Contains(body, "secret.csv") || strings.Contains(body, "/var/drop") {
		t.Fatal("scrape contained a file path")
	}
	missing, err := http.Get("http://" + server.Address() + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	missing.Body.Close()
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown path status = %d", missing.StatusCode)
	}

	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-serveErr:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("metrics server did not stop")
	}
}

func TestNilServerClose(t *testing.T) {
	var server *Server
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
}

func scrape(t *testing.T, address string) string {
	t.Helper()
	url := "http://" + address + "/metrics"
	deadline := time.Now().Add(2 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		response, err := http.Get(url)
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
			t.Fatalf("status %d body %s", response.StatusCode, body)
		}
		if !strings.Contains(response.Header.Get("Content-Type"), "text/plain") &&
			!strings.Contains(response.Header.Get("Content-Type"), "openmetrics") {
			t.Fatalf("content type %q", response.Header.Get("Content-Type"))
		}
		return string(body)
	}
	t.Fatal(last)
	return ""
}
