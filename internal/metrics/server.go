package metrics

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const shutdownTimeout = 5 * time.Second

// ListenHelp is the flag description shared by onderzeeer test and onderzeeerd.
const ListenHelp = "loopback or wildcard address for Prometheus /metrics (for example 127.0.0.1:9090); empty or off disables it"

// FromEnv returns ONDERZEEER_METRICS_LISTEN with surrounding space removed.
func FromEnv() string {
	return strings.TrimSpace(os.Getenv("ONDERZEEER_METRICS_LISTEN"))
}

// Disabled reports whether address leaves the endpoint off.
// Empty, off, disabled, none, and "-" are disabled.
func Disabled(address string) bool {
	switch strings.ToLower(strings.TrimSpace(address)) {
	case "", "off", "disabled", "none", "-":
		return true
	default:
		return false
	}
}

// Server serves GET /metrics. A nil Server means the endpoint is disabled.
type Server struct {
	listener   net.Listener
	httpServer *http.Server
	address    string

	closeOnce sync.Once
	closeErr  error
}

// Listen binds address. A disabled address returns a nil server and a nil
// error. The bind rules match the dashboard: loopback, localhost, or a wildcard.
func Listen(address string, logger *slog.Logger) (*Server, error) {
	if Disabled(address) {
		return nil, nil
	}
	address = strings.TrimSpace(address)
	wildcard, err := validateListenAddress(address)
	if err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("metrics: listen on %s: %w", address, err)
	}
	tcpAddress, ok := listener.Addr().(*net.TCPAddr)
	if !ok || tcpAddress.IP == nil || (!tcpAddress.IP.IsLoopback() && !(wildcard && tcpAddress.IP.IsUnspecified())) {
		_ = listener.Close()
		return nil, fmt.Errorf("metrics: resolved listen address %q is neither loopback nor wildcard", address)
	}
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
	}))
	server := &Server{
		listener: listener,
		address:  listener.Addr().String(),
	}
	server.httpServer = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       time.Minute,
		MaxHeaderBytes:    16 << 10,
	}
	if wildcard {
		logger.Warn("metrics endpoint is listening on all network interfaces; scrapes are unauthenticated, so restrict network access",
			"address", server.address)
	}
	return server, nil
}

func validateListenAddress(address string) (bool, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false, fmt.Errorf("metrics: listen address must be host:port: %w", err)
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return false, nil
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false, fmt.Errorf("metrics: refusing non-loopback listen address %q; use a wildcard listener or scrape locally", address)
	}
	if ip.IsLoopback() {
		return false, nil
	}
	if ip.IsUnspecified() {
		return true, nil
	}
	return false, fmt.Errorf("metrics: refusing non-loopback listen address %q; use a wildcard listener or scrape locally", address)
}

// Address returns the bound TCP address.
func (s *Server) Address() string {
	if s == nil {
		return ""
	}
	return s.address
}

// Serve handles scrapes until ctx is canceled, Close is called, or the listener fails.
func (s *Server) Serve(ctx context.Context) error {
	if s == nil || s.listener == nil || s.httpServer == nil {
		return errors.New("metrics: server is required")
	}
	if ctx == nil {
		return errors.New("metrics: serve context is required")
	}
	done := make(chan error, 1)
	go func() {
		done <- s.httpServer.Serve(s.listener)
	}()
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	shutdownErr := s.httpServer.Shutdown(shutdownCtx)
	if shutdownErr != nil {
		shutdownErr = errors.Join(shutdownErr, s.httpServer.Close())
	}
	serveErr := <-done
	if errors.Is(serveErr, http.ErrServerClosed) || errors.Is(serveErr, net.ErrClosed) {
		serveErr = nil
	}
	return errors.Join(shutdownErr, serveErr)
}

// Close stops the listener. It is safe to call on a nil server and more than once.
func (s *Server) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		var errs []error
		if s.httpServer != nil {
			if err := s.httpServer.Close(); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
				errs = append(errs, err)
			}
		}
		if s.listener != nil {
			if err := s.listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				errs = append(errs, err)
			}
		}
		s.closeErr = errors.Join(errs...)
	})
	return s.closeErr
}
