package loadtest

import (
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"sync"
	"time"
)

// Runner owns the HTTP client and drives the worker pool.
type Runner struct {
	cfg     Config
	client  *http.Client
	limiter *RateLimiter
	picker  *Picker // non-nil when running a multi-request Scenario
}

// WithScenario switches the runner into scenario mode: each request picks
// a weighted-random endpoint from s instead of always hitting cfg.URL.
func (r *Runner) WithScenario(s *Scenario, seed int64) *Runner {
	r.picker = NewPicker(s, seed)
	return r
}

// ProgressFunc is called after every completed request, letting callers
// (e.g. the CLI) render a live progress bar without coupling this package
// to any particular UI.
type ProgressFunc func(done, total int, r Result)

func NewRunner(cfg Config) *Runner {
	transport := &http.Transport{
		MaxIdleConns:        cfg.Concurrency * 2,
		MaxIdleConnsPerHost: cfg.Concurrency * 2,
		IdleConnTimeout:     30 * time.Second,
	}
	if cfg.Insecure {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // #nosec G402 - explicit opt-in flag
	}

	return &Runner{
		cfg: cfg,
		client: &http.Client{
			Timeout:   cfg.Timeout,
			Transport: transport,
		},
		limiter: NewRateLimiter(cfg.RatePerSec),
	}
}

// Run executes the load test to completion (or until ctx is cancelled,
// e.g. by Ctrl+C) and returns the aggregated Report.
//
// Concurrency model:
//   - one "producer" goroutine feeds job indices into `jobs`
//   - `cfg.Concurrency` worker goroutines pull from `jobs`, perform the
//     HTTP request, and push a Result onto `results`
//   - a "closer" goroutine waits for all workers to finish and then
//     closes `results`, so the range loop below terminates cleanly
//   - ctx cancellation (timeout, signal, or duration-based stop) is
//     checked at every blocking point so shutdown is prompt
func (r *Runner) Run(ctx context.Context, progress ProgressFunc) *Report {
	defer r.limiter.Close()

	jobs := make(chan int, r.cfg.Concurrency)
	results := make(chan Result, r.cfg.Concurrency*2)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// If a duration was requested instead of a fixed request count, stop
	// the producer once it elapses.
	if r.cfg.Duration > 0 {
		go func() {
			timer := time.NewTimer(r.cfg.Duration)
			defer timer.Stop()
			select {
			case <-timer.C:
				cancel()
			case <-runCtx.Done():
			}
		}()
	}

	// Producer
	go func() {
		defer close(jobs)
		i := 0
		for {
			if r.cfg.Requests > 0 && i >= r.cfg.Requests {
				return
			}
			select {
			case <-runCtx.Done():
				return
			case jobs <- i:
				i++
			}
		}
	}()

	// Workers
	var wg sync.WaitGroup
	for w := 0; w < r.cfg.Concurrency; w++ {
		wg.Add(1)
		go r.worker(runCtx, jobs, results, &wg)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	collector := NewCollector(r.cfg.URL)
	done := 0
	for res := range results {
		collector.Add(res)
		done++
		if progress != nil {
			progress(done, r.cfg.Requests, res)
		}
	}

	return collector.Report()
}

func (r *Runner) worker(ctx context.Context, jobs <-chan int, results chan<- Result, wg *sync.WaitGroup) {
	defer wg.Done()

	for range jobs {
		if err := r.limiter.Wait(ctx); err != nil {
			results <- Result{Err: err, Timestamp: time.Now()}
			continue
		}

		select {
		case <-ctx.Done():
			return
		default:
		}

		results <- r.do(ctx)
	}
}

func (r *Runner) do(ctx context.Context) Result {
	start := time.Now()

	method, url, body, headers := r.cfg.Method, r.cfg.URL, r.cfg.Body, r.cfg.Headers
	if r.picker != nil {
		sr := r.picker.Pick()
		method, url, body, headers = sr.Method, sr.URL, []byte(sr.Body), ToHeader(sr.Headers)
	}

	var bodyReader io.Reader
	if len(body) > 0 {
		bodyReader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return Result{Err: err, Timestamp: start}
	}
	req.Header = headers.Clone()

	resp, err := r.client.Do(req)
	if err != nil {
		return Result{Err: err, Timestamp: start, Latency: time.Since(start)}
	}
	defer resp.Body.Close()

	n, _ := io.Copy(io.Discard, resp.Body)

	return Result{
		StatusCode: resp.StatusCode,
		Latency:    time.Since(start),
		BytesRead:  n,
		Timestamp:  start,
	}
}
