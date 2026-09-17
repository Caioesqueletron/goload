package loadtest

import (
	"math"
	"sort"
	"sync"
	"time"
)

// Collector aggregates Results coming from many worker goroutines.
// All public methods are safe for concurrent use; internally a single
// mutex protects the slices/maps since the contention point is the
// (much slower) network I/O in the workers, not the bookkeeping here.
type Collector struct {
	mu sync.Mutex

	targetURL string
	start     time.Time
	end       time.Time

	latencies    []time.Duration
	statusCounts map[int]int
	errorSamples map[string]int
	bytesRead    int64
	success      int
	failed       int
}

func NewCollector(targetURL string) *Collector {
	return &Collector{
		targetURL:    targetURL,
		start:        time.Now(),
		statusCounts: make(map[int]int),
		errorSamples: make(map[string]int),
	}
}

// Add records one Result. Called concurrently from many goroutines that
// read off the `results` channel — in practice we funnel all results
// through a single consumer goroutine (see Runner.Run), so contention
// here is minimal, but the lock keeps Collector safe regardless of how
// callers wire it up.
func (c *Collector) Add(r Result) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if r.Err != nil {
		c.failed++
		c.errorSamples[r.Err.Error()]++
		return
	}

	c.success++
	c.statusCounts[r.StatusCode]++
	c.bytesRead += r.BytesRead
	c.latencies = append(c.latencies, r.Latency)
}

// Report finalizes the run and computes percentiles. Call once, after all
// Add calls have completed.
func (c *Collector) Report() *Report {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.end = time.Now()
	total := c.success + c.failed
	dur := c.end.Sub(c.start)

	sorted := make([]time.Duration, len(c.latencies))
	copy(sorted, c.latencies)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	lat := LatencyReport{}
	if len(sorted) > 0 {
		var sum time.Duration
		for _, d := range sorted {
			sum += d
		}
		lat.MinMs = msf(sorted[0])
		lat.MaxMs = msf(sorted[len(sorted)-1])
		lat.MeanMs = msf(sum) / float64(len(sorted))
		lat.P50Ms = msf(percentile(sorted, 50))
		lat.P95Ms = msf(percentile(sorted, 95))
		lat.P99Ms = msf(percentile(sorted, 99))
	}

	errRate := 0.0
	if total > 0 {
		errRate = float64(c.failed) / float64(total) * 100
	}

	throughput := 0.0
	if dur > 0 {
		throughput = float64(total) / dur.Seconds()
	}

	return &Report{
		TargetURL:       c.targetURL,
		TotalRequests:   total,
		SuccessRequests: c.success,
		FailedRequests:  c.failed,
		ErrorRate:       errRate,
		Duration:        dur,
		DurationMillis:  dur.Milliseconds(),
		Throughput:      throughput,
		BytesRead:       c.bytesRead,
		StatusCounts:    copyIntMap(c.statusCounts),
		ErrorSamples:    copyStrMap(c.errorSamples),
		Latency:         lat,
	}
}

// percentile returns the p-th percentile (0-100) of an already sorted
// slice using the "nearest rank" method, which is simple, dependency-free
// and accurate enough for load-testing purposes.
func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Ceil(p/100*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func msf(d time.Duration) float64 {
	return float64(d) / float64(time.Millisecond)
}

func copyIntMap(m map[int]int) map[int]int {
	out := make(map[int]int, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func copyStrMap(m map[string]int) map[string]int {
	out := make(map[string]int, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
