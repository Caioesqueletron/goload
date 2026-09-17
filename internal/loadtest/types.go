package loadtest

import (
	"net/http"
	"time"
)

// Config describes a single load test run. It is deliberately flat so it can
// be built either from CLI flags or from a Scenario file.
type Config struct {
	URL         string
	Method      string
	Headers     http.Header
	Body        []byte
	Requests    int           // total number of requests to fire (0 = unbounded, use Duration)
	Concurrency int           // number of concurrent workers
	Duration    time.Duration // if > 0, run for this long instead of a fixed request count
	Timeout     time.Duration // per-request timeout
	RatePerSec  float64       // 0 = unlimited
	Insecure    bool          // skip TLS verification
}

// Result is what a single worker reports back after executing one request.
type Result struct {
	StatusCode int
	Latency    time.Duration
	BytesRead  int64
	Err        error
	Timestamp  time.Time
}

// Report is the final, aggregated output of a load test run.
type Report struct {
	TargetURL       string             `json:"target_url"`
	TotalRequests   int                `json:"total_requests"`
	SuccessRequests int                `json:"success_requests"`
	FailedRequests  int                `json:"failed_requests"`
	ErrorRate       float64            `json:"error_rate"`
	Duration        time.Duration      `json:"-"`
	DurationMillis  int64              `json:"duration_ms"`
	Throughput      float64            `json:"throughput_rps"`
	BytesRead       int64              `json:"bytes_read"`
	StatusCounts    map[int]int        `json:"status_distribution"`
	ErrorSamples    map[string]int     `json:"error_samples,omitempty"`
	Latency         LatencyReport      `json:"latency"`
}

// LatencyReport holds the percentile breakdown, all in milliseconds for
// readability in JSON/Prometheus output.
type LatencyReport struct {
	MinMs  float64 `json:"min_ms"`
	MeanMs float64 `json:"mean_ms"`
	P50Ms  float64 `json:"p50_ms"`
	P95Ms  float64 `json:"p95_ms"`
	P99Ms  float64 `json:"p99_ms"`
	MaxMs  float64 `json:"max_ms"`
}
