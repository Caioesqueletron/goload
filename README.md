# goload

An educational HTTP load testing CLI — a small, readable alternative to
[`hey`](https://github.com/rakyll/hey) and [k6](https://k6.io/), built with
the Go standard library only. The goal isn't to out-feature those tools; it's
to show, in a codebase small enough to read in one sitting, how a concurrent
load generator is actually put together in Go: worker pools, channels,
`context` cancellation, a home-grown rate limiter, and metrics export.

## Quick start

```bash
go build -o goload ./cmd/goload

./goload -url https://api.example.com/users -requests 10000 -concurrency 100
```

```
Target:         https://api.example.com/users
Duration:       4.82s
Requests:       10000
Success:        9978
Failed:         22
Error rate:     0.22%
Throughput:     2074.68 req/s

Latency:
  min:  8.1ms
  mean: 47.3ms
  p50:  41.0ms
  p95:  112.4ms
  p99:  201.7ms
  max:  980.2ms

Status distribution:
  200: 9978
```

## Features

| Feature                      | Flag(s)                                    |
|-------------------------------|---------------------------------------------|
| Concurrent requests           | `-concurrency`                              |
| Fixed request count or duration | `-requests`, `-duration`                  |
| Latency p50 / p95 / p99       | always computed, shown in text/JSON/Prometheus |
| Throughput (req/s)            | always computed                             |
| Error rate & error samples    | always computed                             |
| HTTP status distribution      | always computed                             |
| JSON output                   | `-output json`                              |
| Prometheus export             | `-prometheus-addr :9090` (scrape `/metrics` live, mid-run) |
| Client-side rate limiting     | `-rate 500` (requests/sec across all workers) |
| Configurable scenarios        | `-scenario examples/scenario.json` (weighted mix of endpoints) |
| Custom headers / method / body | `-header`, `-method`, `-body`              |
| Graceful Ctrl+C shutdown      | built-in, via `context`                     |

## Architecture

```
cmd/goload/main.go          CLI: flag parsing, signal handling, rendering
internal/loadtest/
  types.go                  Config, Result, Report — the data contracts
  runner.go                 The concurrency engine (see below)
  stats.go                  Thread-safe Collector + percentile math
  ratelimiter.go             Token-bucket rate limiter (stdlib only)
  scenario.go                Weighted multi-endpoint scenarios (JSON)
  export.go                  JSON + Prometheus text-format exporters
```

### The concurrency model

```
            +-----------+        jobs (chan int)        +---------+
producer -->|  jobs ch  |------------------------------->| worker  |--+
 goroutine  +-----------+                                +---------+  |
                                                          +---------+  |
                                                     ...  | worker  |  |  results (chan Result)
                                                          +---------+  |
                                                                       v
                                                              +----------------+
                                                              |   Collector    |
                                                              | (percentiles,  |
                                                              |  status codes, |
                                                              |  error rate)   |
                                                              +----------------+
```

* A **producer goroutine** feeds job indices (or "keep going" signals, in
  duration mode) into a buffered `jobs` channel.
* A pool of exactly `-concurrency` **worker goroutines** pull from `jobs`,
  optionally block on the rate limiter, execute the HTTP request, and push
  a `Result` onto a `results` channel. This is the classic bounded
  worker-pool pattern — concurrency is capped by the number of goroutines,
  not by spawning one goroutine per request.
* A small **closer goroutine** calls `wg.Wait()` and then closes `results`,
  which is what lets the main goroutine's `for res := range results`
  terminate cleanly instead of blocking forever.
* Every blocking operation (`jobs <- i`, rate limiter `Wait`, the HTTP
  round trip via `http.NewRequestWithContext`) is wired to a single
  `context.Context`. Ctrl+C (or `-duration` elapsing) cancels that context,
  so an interrupted run stops promptly *and* still prints an accurate
  report for whatever completed before cancellation — nothing is silently
  dropped.
* `Collector` is the single point of shared mutable state; it's guarded by
  one `sync.Mutex`. Contention is a non-issue in practice because network
  I/O (milliseconds) dwarfs the cost of a mutex-protected `append` and map
  increment (nanoseconds).

### Rate limiting

`internal/loadtest/ratelimiter.go` implements a minimal token bucket by
hand instead of pulling in `golang.org/x/time/rate`, on purpose — it's
~50 lines and demonstrates the pattern: a `time.Ticker` refills a
buffered channel of size 1, and workers call `Wait(ctx)` which blocks on
either receiving a token or the context being cancelled.

### Scenarios

Real traffic is rarely a single endpoint hammered in a loop. A `Scenario`
(`examples/scenario.json`) describes several named requests with relative
weights; `Picker` does weighted-random selection so, e.g., 70% of traffic
hits `GET /users` and 10% hits `POST /users`, matching a realistic mix.

### Metrics export

* `-output json` dumps the final `Report` as JSON — handy for piping into
  `jq` or storing in CI artifacts.
* `-prometheus-addr :9090` starts an HTTP server exposing `/metrics` in
  Prometheus text format *while the test is running*, updated after every
  request — useful for watching a long soak test in Grafana instead of
  only getting a summary at the end.

## What this demonstrates

- Bounded worker pools with goroutines + channels (not one goroutine per request)
- `context.Context` for cooperative cancellation across producer, workers, and rate limiter
- Safe concurrent aggregation (`sync.Mutex`-guarded Collector) vs. one-result-at-a-time channel consumption
- A hand-rolled token-bucket rate limiter
- Streaming percentile computation over sorted latency samples
- Pluggable output formats (text / JSON / Prometheus) behind a stable `Report` contract

## Limitations (by design, for an educational project)

- Percentiles are computed from all retained samples at the end of the run
  (O(n log n) sort), not with a streaming/HDR-histogram algorithm — fine up
  to a few million requests, but a production tool would use something
  like [HDRHistogram](https://github.com/HdrHistogram/hdrhistogram-go) to
  bound memory.
- No HTTP/2 multiplexing tuning, no distributed/multi-machine coordination.
- Scenario files are JSON, not YAML, to keep the dependency graph at zero.
