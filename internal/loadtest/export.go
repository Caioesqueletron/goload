package loadtest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"
)

// WriteJSON serializes a Report as pretty-printed JSON.
func WriteJSON(w io.Writer, r *Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// WritePrometheus renders a Report in the Prometheus text exposition
// format (https://prometheus.io/docs/instrumenting/exposition_formats/).
func WritePrometheus(w io.Writer, r *Report) error {
	lines := []string{
		metric("goload_requests_total", "counter", "Total requests fired", float64(r.TotalRequests)),
		metric("goload_requests_success_total", "counter", "Successful requests", float64(r.SuccessRequests)),
		metric("goload_requests_failed_total", "counter", "Failed requests", float64(r.FailedRequests)),
		metric("goload_error_rate", "gauge", "Error rate as a percentage", r.ErrorRate),
		metric("goload_throughput_rps", "gauge", "Requests per second achieved", r.Throughput),
		metric("goload_bytes_read_total", "counter", "Total response bytes read", float64(r.BytesRead)),
		metric("goload_latency_min_ms", "gauge", "Minimum latency in ms", r.Latency.MinMs),
		metric("goload_latency_mean_ms", "gauge", "Mean latency in ms", r.Latency.MeanMs),
		metric("goload_latency_p50_ms", "gauge", "p50 latency in ms", r.Latency.P50Ms),
		metric("goload_latency_p95_ms", "gauge", "p95 latency in ms", r.Latency.P95Ms),
		metric("goload_latency_p99_ms", "gauge", "p99 latency in ms", r.Latency.P99Ms),
		metric("goload_latency_max_ms", "gauge", "Maximum latency in ms", r.Latency.MaxMs),
	}

	for _, l := range lines {
		if _, err := io.WriteString(w, l); err != nil {
			return err
		}
	}

	// Status code distribution as a labeled metric.
	if _, err := io.WriteString(w, "# HELP goload_status_total Responses received per HTTP status code\n# TYPE goload_status_total counter\n"); err != nil {
		return err
	}
	codes := make([]int, 0, len(r.StatusCounts))
	for c := range r.StatusCounts {
		codes = append(codes, c)
	}
	sort.Ints(codes)
	for _, c := range codes {
		if _, err := fmt.Fprintf(w, "goload_status_total{code=\"%d\"} %d\n", c, r.StatusCounts[c]); err != nil {
			return err
		}
	}
	return nil
}

func metric(name, mtype, help string, value float64) string {
	return fmt.Sprintf("# HELP %s %s\n# TYPE %s %s\n%s %v\n", name, help, name, mtype, name, value)
}

// MetricsServer exposes the most recent Report at /metrics in Prometheus
// format, so goload can run as a long-lived load generator that a real
// Prometheus instance scrapes mid-run (e.g. for soak tests).
type MetricsServer struct {
	mu     sync.RWMutex
	latest *Report
}

func NewMetricsServer() *MetricsServer {
	return &MetricsServer{}
}

func (m *MetricsServer) Update(r *Report) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.latest = r
}

func (m *MetricsServer) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	m.mu.RLock()
	r := m.latest
	m.mu.RUnlock()

	if r == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("# no data yet\n"))
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	_ = WritePrometheus(w, r)
}

// Serve starts the metrics HTTP server in the background and returns
// immediately; callers should stop it by cancelling ctx or letting main()
// exit.
func (m *MetricsServer) Serve(addr string) *http.Server {
	mux := http.NewServeMux()
	mux.Handle("/metrics", m)
	srv := &http.Server{Addr: addr, Handler: mux}
	go func() {
		_ = srv.ListenAndServe()
	}()
	return srv
}
