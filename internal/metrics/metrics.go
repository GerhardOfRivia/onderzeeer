// Package metrics records Prometheus series for onderzeeer.
//
// Label sets are fixed. Paths, watch names, instance names, command names, and
// job ids are not labels. An unexpected label value is collapsed to "other",
// so a caller cannot create a series per file or request path.
package metrics

import (
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	dto "github.com/prometheus/client_model/go"
)

// longBuckets spans a short command through a multi-hour pipeline step.
var longBuckets = []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300, 600, 1800, 3600, 7200}

// httpBuckets spans a static asset through a slow queue overview.
var httpBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30}

var httpRoutes = map[string]struct{}{
	"page":                                  {},
	"asset":                                 {},
	"docs":                                  {},
	"unmatched":                             {},
	"other":                                 {},
	"/api/v1/pipelines/validate":            {},
	"/api/v1/pipelines/parse":               {},
	"/api/v1/info":                          {},
	"/api/v1/queues":                        {},
	"/api/v1/queues/{queueID}/jobs":         {},
	"/api/v1/queues/{queueID}/jobs/{jobID}": {},
	"/api/v1/queues/{queueID}/events":       {},
	"/api/v1/queues/{queueID}/commands/{commandID}/output": {},
	"/api/v1/instances":                   {},
	"/api/v1/queues/{queueID}/start":      {},
	"/api/v1/instances/{instanceID}/stop": {},
}

var (
	registry = prometheus.NewRegistry()

	httpRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "onderzeeer_http_requests_total",
		Help: "HTTP requests handled by the dashboard listener, by method, route, and status class.",
	}, []string{"method", "route", "code"})

	httpDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "onderzeeer_http_request_duration_seconds",
		Help:    "Seconds to handle one dashboard HTTP request, by method and route.",
		Buckets: httpBuckets,
	}, []string{"method", "route"})

	files = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "onderzeeer_files_total",
		Help: "Stable files finished by a watcher. result is delivered or skipped.",
	}, []string{"result"})

	watcherErrors = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "onderzeeer_watcher_errors_total",
		Help: "Filesystem watcher failures reported to the daemon log. Cancellation is not included.",
	})

	filesPending = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "onderzeeer_files_pending",
		Help: "Files currently waiting to settle before they can be queued.",
	})

	jobsEnqueued = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "onderzeeer_jobs_enqueued_total",
		Help: "Enqueue attempts for a settled file. result is created or duplicate.",
	}, []string{"result"})

	jobsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "onderzeeer_jobs_total",
		Help: "Pipeline attempts by outcome (succeeded, failed, retried, or interrupted).",
	}, []string{"outcome"})

	jobDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "onderzeeer_job_duration_seconds",
		Help:    "Seconds spent on one pipeline attempt, including its commands.",
		Buckets: longBuckets,
	}, []string{"outcome"})

	jobsActive = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "onderzeeer_jobs_active",
		Help: "Pipeline attempts currently executing.",
	})

	jobsWaiting = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "onderzeeer_jobs_waiting_resources",
		Help: "Pipeline attempts currently waiting for a shared resource.",
	})

	jobsRecovered = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "onderzeeer_jobs_recovered_total",
		Help: "Running or pending jobs requeued after a process restart.",
	})

	commandsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "onderzeeer_commands_total",
		Help: "Pipeline commands that finished, by executor and result (succeeded or failed).",
	}, []string{"executor", "result"})

	commandDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "onderzeeer_command_duration_seconds",
		Help:    "Seconds to run one pipeline command, by executor and result.",
		Buckets: longBuckets,
	}, []string{"executor", "result"})

	commandsActive = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "onderzeeer_commands_active",
		Help: "Pipeline commands currently running.",
	})

	resourceWaits = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "onderzeeer_resource_waits_total",
		Help: "Shared resource waits by result (acquired or canceled).",
	}, []string{"result"})

	resourceWaitDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "onderzeeer_resource_wait_duration_seconds",
		Help:    "Seconds spent waiting for a shared resource, by result.",
		Buckets: longBuckets,
	}, []string{"result"})

	instancesTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "onderzeeer_instances_total",
		Help: "Daemon-managed instances that stopped, by outcome (exited or failed).",
	}, []string{"outcome"})

	instanceDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "onderzeeer_instance_duration_seconds",
		Help:    "Seconds a daemon-managed instance ran, by outcome.",
		Buckets: longBuckets,
	}, []string{"outcome"})

	instancesActive = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "onderzeeer_instances_active",
		Help: "Daemon-managed instances currently running. Zero for onderzeeer test.",
	})

	buildInfo = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "onderzeeer_build_info",
		Help: "Build information for this process. The value is always 1.",
	}, []string{"version"})
)

func init() {
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		httpRequests,
		httpDuration,
		files,
		watcherErrors,
		filesPending,
		jobsEnqueued,
		jobsTotal,
		jobDuration,
		jobsActive,
		jobsWaiting,
		jobsRecovered,
		commandsTotal,
		commandDuration,
		commandsActive,
		resourceWaits,
		resourceWaitDuration,
		instancesTotal,
		instanceDuration,
		instancesActive,
		buildInfo,
	)
	SetVersion("dev")
	primeSeries()
}

// primeSeries creates the fixed label sets so a fresh process exports zeros
// instead of omitting the series until the first event.
func primeSeries() {
	for _, result := range []string{"delivered", "skipped"} {
		files.WithLabelValues(result)
	}
	for _, result := range []string{"created", "duplicate"} {
		jobsEnqueued.WithLabelValues(result)
	}
	for _, outcome := range []string{"succeeded", "failed", "retried", "interrupted"} {
		jobsTotal.WithLabelValues(outcome)
		jobDuration.WithLabelValues(outcome)
	}
	for _, executor := range []string{"command", "shell", "docker", "podman", "apptainer"} {
		for _, result := range []string{"succeeded", "failed"} {
			commandsTotal.WithLabelValues(executor, result)
			commandDuration.WithLabelValues(executor, result)
		}
	}
	for _, result := range []string{"acquired", "canceled"} {
		resourceWaits.WithLabelValues(result)
		resourceWaitDuration.WithLabelValues(result)
	}
	for _, outcome := range []string{"exited", "failed"} {
		instancesTotal.WithLabelValues(outcome)
		instanceDuration.WithLabelValues(outcome)
	}
}

// SetVersion records the running binary version on onderzeeer_build_info.
func SetVersion(version string) {
	version = strings.TrimSpace(version)
	if version == "" {
		version = "dev"
	}
	buildInfo.Reset()
	buildInfo.WithLabelValues(version).Set(1)
}

// ObserveHTTP records one dashboard request. route must already be a fixed
// class or API pattern; anything else is reported as other.
func ObserveHTTP(method, route string, status int, elapsed time.Duration) {
	method = methodOf(method)
	route = routeOf(route)
	httpRequests.WithLabelValues(method, route, codeOf(status)).Inc()
	httpDuration.WithLabelValues(method, route).Observe(seconds(elapsed))
}

// File counts one settled file. result is delivered or skipped.
func File(result string) {
	files.WithLabelValues(fileResult(result)).Inc()
}

// WatcherError counts one reported filesystem watcher failure.
func WatcherError() {
	watcherErrors.Inc()
}

// FilesPending adjusts the number of files waiting to settle.
func FilesPending(delta int) {
	if delta == 0 {
		return
	}
	filesPending.Add(float64(delta))
}

// JobEnqueued counts one enqueue attempt. result is created or duplicate.
func JobEnqueued(result string) {
	jobsEnqueued.WithLabelValues(enqueueResult(result)).Inc()
}

// JobsActive adjusts the number of pipeline attempts in progress.
func JobsActive(delta int) {
	if delta == 0 {
		return
	}
	jobsActive.Add(float64(delta))
}

// JobsWaiting adjusts the number of attempts blocked on a shared resource.
func JobsWaiting(delta int) {
	if delta == 0 {
		return
	}
	jobsWaiting.Add(float64(delta))
}

// ObserveJob counts one finished pipeline attempt and records how long it took.
func ObserveJob(outcome string, elapsed time.Duration) {
	outcome = jobOutcome(outcome)
	jobsTotal.WithLabelValues(outcome).Inc()
	jobDuration.WithLabelValues(outcome).Observe(seconds(elapsed))
}

// AddRecovered counts jobs requeued because a previous process stopped while
// they were running or pending.
func AddRecovered(n int64) {
	if n <= 0 {
		return
	}
	jobsRecovered.Add(float64(n))
}

// CommandsActive adjusts the number of pipeline commands in progress.
func CommandsActive(delta int) {
	if delta == 0 {
		return
	}
	commandsActive.Add(float64(delta))
}

// ObserveCommand counts one finished command and records how long it took.
func ObserveCommand(executor, result string, elapsed time.Duration) {
	executor = executorOf(executor)
	result = commandResult(result)
	commandsTotal.WithLabelValues(executor, result).Inc()
	commandDuration.WithLabelValues(executor, result).Observe(seconds(elapsed))
}

// ObserveResourceWait counts one shared resource wait and records how long it took.
func ObserveResourceWait(result string, elapsed time.Duration) {
	result = waitResult(result)
	resourceWaits.WithLabelValues(result).Inc()
	resourceWaitDuration.WithLabelValues(result).Observe(seconds(elapsed))
}

// InstanceStarted records that a daemon-managed instance is now running.
func InstanceStarted() {
	instancesActive.Inc()
}

// InstanceFinished records that a daemon-managed instance stopped.
func InstanceFinished(outcome string, elapsed time.Duration) {
	instancesActive.Dec()
	outcome = instanceOutcome(outcome)
	instancesTotal.WithLabelValues(outcome).Inc()
	instanceDuration.WithLabelValues(outcome).Observe(seconds(elapsed))
}

// Sample returns the current value of a counter or gauge, or the number of
// observations of a histogram. It returns 0 when the series is absent.
func Sample(name string, labels map[string]string) float64 {
	families, err := registry.Gather()
	if err != nil {
		return 0
	}
	if labels == nil {
		labels = map[string]string{}
	}
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.GetMetric() {
			if !labelsEqual(metric, labels) {
				continue
			}
			switch {
			case metric.GetCounter() != nil:
				return metric.GetCounter().GetValue()
			case metric.GetGauge() != nil:
				return metric.GetGauge().GetValue()
			case metric.GetHistogram() != nil:
				return float64(metric.GetHistogram().GetSampleCount())
			}
		}
	}
	return 0
}

func labelsEqual(metric *dto.Metric, labels map[string]string) bool {
	got := metric.GetLabel()
	if len(got) != len(labels) {
		return false
	}
	for _, label := range got {
		value, ok := labels[label.GetName()]
		if !ok || value != label.GetValue() {
			return false
		}
	}
	return true
}

func seconds(elapsed time.Duration) float64 {
	if elapsed < 0 {
		return 0
	}
	return elapsed.Seconds()
}

func methodOf(method string) string {
	switch strings.ToUpper(strings.TrimSpace(method)) {
	case "GET", "POST", "HEAD":
		return strings.ToUpper(strings.TrimSpace(method))
	default:
		return "other"
	}
}

func routeOf(route string) string {
	if _, ok := httpRoutes[route]; ok {
		return route
	}
	return "other"
}

func codeOf(status int) string {
	switch {
	case status >= 200 && status < 300:
		return "2xx"
	case status >= 300 && status < 400:
		return "3xx"
	case status >= 400 && status < 500:
		return "4xx"
	case status >= 500 && status < 600:
		return "5xx"
	default:
		return "other"
	}
}

func fileResult(result string) string {
	switch result {
	case "delivered", "skipped":
		return result
	default:
		return "other"
	}
}

func enqueueResult(result string) string {
	switch result {
	case "created", "duplicate":
		return result
	default:
		return "other"
	}
}

func jobOutcome(outcome string) string {
	switch outcome {
	case "succeeded", "failed", "retried", "interrupted":
		return outcome
	default:
		return "other"
	}
}

func executorOf(executor string) string {
	switch strings.ToLower(strings.TrimSpace(executor)) {
	case "", "command":
		return "command"
	case "shell", "docker", "podman", "apptainer":
		return strings.ToLower(strings.TrimSpace(executor))
	default:
		return "other"
	}
}

func commandResult(result string) string {
	switch result {
	case "succeeded", "failed":
		return result
	default:
		return "other"
	}
}

func waitResult(result string) string {
	switch result {
	case "acquired", "canceled":
		return result
	default:
		return "other"
	}
}

func instanceOutcome(outcome string) string {
	switch outcome {
	case "exited", "failed":
		return outcome
	default:
		return "other"
	}
}
