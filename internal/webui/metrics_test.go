package webui

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GerhardOfRivia/onderzeeer/internal/control"
	"github.com/GerhardOfRivia/onderzeeer/internal/metrics"
)

func TestDashboardRequestsAreMeasuredWithoutPathLabels(t *testing.T) {
	manager, err := control.NewManager(control.Options{Logger: testLogger()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = manager.Shutdown(ctx)
	})
	tokenDirectory := t.TempDir()
	if err := os.Chmod(tokenDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer("127.0.0.1:0", filepath.Join(tokenDirectory, "web.token"), "dev", manager, testLogger(), false)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(ctx) }()
	defer server.Close()

	metricsServer, err := metrics.Listen("127.0.0.1:0", nil)
	if err != nil {
		t.Fatal(err)
	}
	metricsErr := make(chan error, 1)
	go func() { metricsErr <- metricsServer.Serve(ctx) }()
	defer metricsServer.Close()

	secret := "secret-asset-name.js"
	pageBefore := metrics.Sample("onderzeeer_http_requests_total", map[string]string{"method": "GET", "route": "page", "code": "2xx"})
	assetBefore := metrics.Sample("onderzeeer_http_requests_total", map[string]string{"method": "GET", "route": "asset", "code": "4xx"})
	apiBefore := metrics.Sample("onderzeeer_http_requests_total", map[string]string{"method": "GET", "route": "/api/v1/info", "code": "4xx"})

	pageResponse, err := http.Get("http://" + server.Address() + "/")
	if err != nil {
		t.Fatal(err)
	}
	pageResponse.Body.Close()
	if pageResponse.StatusCode != http.StatusOK {
		t.Fatalf("page status = %d", pageResponse.StatusCode)
	}
	assetResponse, err := http.Get("http://" + server.Address() + "/assets/" + secret)
	if err != nil {
		t.Fatal(err)
	}
	assetResponse.Body.Close()
	infoResponse, err := http.Get("http://" + server.Address() + "/api/v1/info")
	if err != nil {
		t.Fatal(err)
	}
	infoResponse.Body.Close()
	if infoResponse.StatusCode != http.StatusUnauthorized {
		t.Fatalf("info status = %d", infoResponse.StatusCode)
	}

	if metrics.Sample("onderzeeer_http_requests_total", map[string]string{"method": "GET", "route": "page", "code": "2xx"}) != pageBefore+1 {
		t.Fatal("dashboard page was not counted")
	}
	if metrics.Sample("onderzeeer_http_requests_total", map[string]string{"method": "GET", "route": "asset", "code": "4xx"}) != assetBefore+1 {
		t.Fatal("missing asset was not counted")
	}
	if metrics.Sample("onderzeeer_http_requests_total", map[string]string{"method": "GET", "route": "/api/v1/info", "code": "4xx"}) != apiBefore+1 {
		t.Fatal("API route was not counted")
	}
	if metrics.Sample("onderzeeer_http_request_duration_seconds", map[string]string{"method": "GET", "route": "page"}) < 1 {
		t.Fatal("request duration was not recorded")
	}

	body := scrapeWebMetrics(t, metricsServer.Address())
	if strings.Contains(body, secret) {
		t.Fatal("metrics contained an asset filename")
	}
	metricsResponse, err := http.Get("http://" + server.Address() + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	dashboardBody, err := io.ReadAll(metricsResponse.Body)
	metricsResponse.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(dashboardBody), "onderzeeer_build_info") {
		t.Fatal("dashboard listener served Prometheus metrics")
	}

	cancel()
	_ = server.Close()
	_ = metricsServer.Close()
	select {
	case err := <-serveErr:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("web server did not stop")
	}
	select {
	case err := <-metricsErr:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("metrics server did not stop")
	}
}

func TestRegisteredAPIRoutesStayMeasurable(t *testing.T) {
	api := apiServer{}
	for pattern := range api.routes() {
		method, path, ok := strings.Cut(pattern, " ")
		if !ok {
			t.Fatalf("route %q has no method", pattern)
		}
		code := "2xx"
		if method != http.MethodGet && method != http.MethodPost && method != http.MethodHead {
			code = "2xx"
		}
		before := metrics.Sample("onderzeeer_http_requests_total", map[string]string{"method": method, "route": path, "code": code})
		metrics.ObserveHTTP(method, path, http.StatusOK, time.Millisecond)
		if metrics.Sample("onderzeeer_http_requests_total", map[string]string{"method": method, "route": path, "code": code}) != before+1 {
			t.Fatalf("route %s was collapsed", pattern)
		}
	}
}

func scrapeWebMetrics(t *testing.T, address string) string {
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
			t.Fatalf("status %d", response.StatusCode)
		}
		return string(body)
	}
	t.Fatal(last)
	return ""
}
