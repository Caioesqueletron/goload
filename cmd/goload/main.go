// Command goload is a small, educational HTTP load testing CLI — an
// alternative to k6/hey focused on being readable Go rather than
// feature-complete. See README.md for architecture notes.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/example/goload/internal/loadtest"
)

type headerFlags []string

func (h *headerFlags) String() string { return strings.Join(*h, ",") }
func (h *headerFlags) Set(v string) error {
	*h = append(*h, v)
	return nil
}

func main() {
	var (
		url           = flag.String("url", "", "target URL (required unless -scenario is used)")
		method        = flag.String("method", http.MethodGet, "HTTP method")
		requests      = flag.Int("requests", 100, "total number of requests (0 = unbounded, use -duration)")
		concurrency   = flag.Int("concurrency", 10, "number of concurrent workers")
		duration      = flag.Duration("duration", 0, "run for this long instead of a fixed request count, e.g. 30s")
		timeout       = flag.Duration("timeout", 10*time.Second, "per-request timeout")
		rate          = flag.Float64("rate", 0, "max requests/sec across all workers (0 = unlimited)")
		body          = flag.String("body", "", "request body")
		insecure      = flag.Bool("insecure", false, "skip TLS certificate verification")
		output        = flag.String("output", "text", "output format: text | json")
		promAddr      = flag.String("prometheus-addr", "", "if set, serve live metrics at http://ADDR/metrics during the run")
		scenarioPath  = flag.String("scenario", "", "path to a JSON scenario file (mixes several weighted requests)")
		quiet         = flag.Bool("quiet", false, "suppress the live progress line")
	)
	var headers headerFlags
	flag.Var(&headers, "header", "extra request header, e.g. -header 'Authorization: Bearer xyz' (repeatable)")
	flag.Parse()

	if *url == "" && *scenarioPath == "" {
		fmt.Fprintln(os.Stderr, "error: -url or -scenario is required")
		flag.Usage()
		os.Exit(2)
	}

	hdr := http.Header{}
	for _, h := range headers {
		parts := strings.SplitN(h, ":", 2)
		if len(parts) != 2 {
			fmt.Fprintf(os.Stderr, "error: invalid -header %q, expected 'Key: Value'\n", h)
			os.Exit(2)
		}
		hdr.Set(strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]))
	}

	cfg := loadtest.Config{
		URL:         *url,
		Method:      strings.ToUpper(*method),
		Headers:     hdr,
		Body:        []byte(*body),
		Requests:    *requests,
		Concurrency: *concurrency,
		Duration:    *duration,
		Timeout:     *timeout,
		RatePerSec:  *rate,
		Insecure:    *insecure,
	}

	var scenario *loadtest.Scenario
	if *scenarioPath != "" {
		sc, err := loadtest.LoadScenario(*scenarioPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		scenario = sc
		cfg.URL = fmt.Sprintf("scenario:%s", sc.Name)
		fmt.Fprintf(os.Stderr, "running scenario %q (%d request types)\n", sc.Name, len(sc.Requests))
	}

	runner := loadtest.NewRunner(cfg)
	if scenario != nil {
		runner = runner.WithScenario(scenario, time.Now().UnixNano())
	}

	// Graceful shutdown on Ctrl+C / SIGTERM: the context passed to
	// runner.Run is cancelled, which the producer/worker goroutines check
	// at every blocking point, so an interrupted run still prints a
	// partial (but accurate) report instead of just dying.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var metricsSrv *http.Server
	var ms *loadtest.MetricsServer
	if *promAddr != "" {
		ms = loadtest.NewMetricsServer()
		metricsSrv = ms.Serve(*promAddr)
		fmt.Fprintf(os.Stderr, "prometheus metrics: http://%s/metrics\n", *promAddr)
	}

	start := time.Now()
	report := runner.Run(ctx, progressPrinter(*quiet, cfg.Requests))
	report.Duration = time.Since(start)
	report.DurationMillis = report.Duration.Milliseconds()

	if ms != nil {
		ms.Update(report)
	}
	if metricsSrv != nil {
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = metricsSrv.Shutdown(shutdownCtx)
		}()
	}

	switch *output {
	case "json":
		if err := loadtest.WriteJSON(os.Stdout, report); err != nil {
			fmt.Fprintln(os.Stderr, "error writing JSON:", err)
			os.Exit(1)
		}
	default:
		printText(report)
	}
}

func progressPrinter(quiet bool, total int) loadtest.ProgressFunc {
	if quiet {
		return nil
	}
	last := time.Now()
	return func(done, total int, r loadtest.Result) {
		if time.Since(last) < 200*time.Millisecond {
			return
		}
		last = time.Now()
		if total > 0 {
			fmt.Fprintf(os.Stderr, "\r%d/%d requests...", done, total)
		} else {
			fmt.Fprintf(os.Stderr, "\r%d requests...", done)
		}
	}
}

func printText(r *loadtest.Report) {
	fmt.Println()
	fmt.Println("Target:        ", r.TargetURL)
	fmt.Printf("Duration:       %.2fs\n", r.Duration.Seconds())
	fmt.Println("Requests:      ", r.TotalRequests)
	fmt.Println("Success:       ", r.SuccessRequests)
	fmt.Println("Failed:        ", r.FailedRequests)
	fmt.Printf("Error rate:     %.2f%%\n", r.ErrorRate)
	fmt.Printf("Throughput:     %.2f req/s\n", r.Throughput)
	fmt.Println()
	fmt.Println("Latency:")
	fmt.Printf("  min:  %.1fms\n", r.Latency.MinMs)
	fmt.Printf("  mean: %.1fms\n", r.Latency.MeanMs)
	fmt.Printf("  p50:  %.1fms\n", r.Latency.P50Ms)
	fmt.Printf("  p95:  %.1fms\n", r.Latency.P95Ms)
	fmt.Printf("  p99:  %.1fms\n", r.Latency.P99Ms)
	fmt.Printf("  max:  %.1fms\n", r.Latency.MaxMs)
	fmt.Println()
	fmt.Println("Status distribution:")
	for code, count := range r.StatusCounts {
		fmt.Printf("  %d: %d\n", code, count)
	}
	if len(r.ErrorSamples) > 0 {
		fmt.Println()
		fmt.Println("Errors:")
		for msg, count := range r.ErrorSamples {
			fmt.Printf("  %dx %s\n", count, msg)
		}
	}
}
