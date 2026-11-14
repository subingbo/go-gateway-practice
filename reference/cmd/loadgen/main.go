package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

type headerList []string

func (h *headerList) String() string {
	return strings.Join(*h, ", ")
}

func (h *headerList) Set(value string) error {
	*h = append(*h, value)
	return nil
}

func main() {
	target := flag.String("url", "", "target URL")
	requests := flag.Int("requests", 100, "total number of requests")
	concurrency := flag.Int("concurrency", 10, "number of concurrent workers")
	rps := flag.Int("rps", 0, "requests per second (0 = unlimited)")
	bearer := flag.String("bearer", "", "bearer token")
	timeout := flag.Duration("timeout", 5*time.Second, "per-request timeout")
	var headers headerList
	flag.Var(&headers, "header", "additional header, e.g. 'X-Canary: true'")
	flag.Parse()

	if *target == "" {
		fmt.Fprintln(os.Stderr, "--url is required")
		os.Exit(2)
	}
	if *requests <= 0 {
		fmt.Fprintln(os.Stderr, "--requests must be positive")
		os.Exit(2)
	}
	if *concurrency <= 0 {
		*concurrency = 1
	}

	headerMap := make(map[string]string)
	for _, raw := range headers {
		key, value, ok := strings.Cut(raw, ":")
		if !ok {
			fmt.Fprintf(os.Stderr, "invalid header %q\n", raw)
			os.Exit(2)
		}
		headerMap[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}

	client := &http.Client{
		Timeout: *timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	type result struct {
		status   int
		duration time.Duration
		err      string
	}

	jobs := make(chan int)
	results := make(chan result, *concurrency)

	var workers sync.WaitGroup

	for i := 0; i < *concurrency; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()

			for range jobs {
				start := time.Now()

				request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, *target, nil)
				if err != nil {
					results <- result{duration: time.Since(start), err: err.Error()}
					continue
				}

				for key, value := range headerMap {
					request.Header.Set(key, value)
				}
				if *bearer != "" {
					request.Header.Set("Authorization", "Bearer "+*bearer)
				}

				response, err := client.Do(request)
				if err != nil {
					results <- result{duration: time.Since(start), err: err.Error()}
					continue
				}

				_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
				_ = response.Body.Close()

				results <- result{status: response.StatusCode, duration: time.Since(start)}
			}
		}()
	}

	done := make(chan struct{})
	go func() {
		workers.Wait()
		close(results)
		close(done)
	}()

	var tokens chan struct{}
	stopTokens := make(chan struct{})

	if *rps > 0 {
		tokens = make(chan struct{}, *concurrency)
		interval := time.Second / time.Duration(*rps)
		if interval <= 0 {
			interval = time.Nanosecond
		}

		go func() {
			ticker := time.NewTicker(interval)
			defer ticker.Stop()

			for {
				select {
				case <-stopTokens:
					return
				case <-ticker.C:
					select {
					case tokens <- struct{}{}:
					default:
					}
				}
			}
		}()
	}

	go func() {
		for i := 0; i < *requests; i++ {
			if tokens != nil {
				<-tokens
			}
			jobs <- i
		}
		close(jobs)
		close(stopTokens)
	}()

	statuses := make(map[int]int)
	var durations []time.Duration
	var errors []string
	success := 0
	failed := 0

	for result := range results {
		if result.err != "" {
			failed++
			if len(errors) < 3 {
				errors = append(errors, result.err)
			}
			continue
		}

		statuses[result.status]++
		durations = append(durations, result.duration)

		if result.status >= 200 && result.status < 400 {
			success++
		} else {
			failed++
		}
	}

	<-done

	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })

	fmt.Printf("target: %s\n", *target)
	fmt.Printf("requests: %d  concurrency: %d  rps: %d\n", *requests, *concurrency, *rps)

	keys := make([]int, 0, len(statuses))
	for status := range statuses {
		keys = append(keys, status)
	}
	sort.Ints(keys)

	fmt.Print("status: ")
	for i, status := range keys {
		if i > 0 {
			fmt.Print("  ")
		}
		fmt.Printf("%d=%d", status, statuses[status])
	}
	fmt.Println()

	fmt.Printf("success: %d (%.1f%%)  failed: %d\n", success, percent(success, *requests), failed)

	if len(durations) > 0 {
		fmt.Printf("latency: min=%s avg=%s p50=%s p95=%s max=%s\n",
			durations[0].Round(time.Microsecond),
			average(durations).Round(time.Microsecond),
			percentile(durations, 50).Round(time.Microsecond),
			percentile(durations, 95).Round(time.Microsecond),
			durations[len(durations)-1].Round(time.Microsecond),
		)
	}

	for _, message := range errors {
		fmt.Printf("error: %s\n", message)
	}
}

func percent(part, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(part) / float64(total) * 100
}

func average(values []time.Duration) time.Duration {
	var total time.Duration
	for _, value := range values {
		total += value
	}
	return total / time.Duration(len(values))
}

func percentile(values []time.Duration, p int) time.Duration {
	if len(values) == 0 {
		return 0
	}

	index := (p*len(values) + 99) / 100
	if index < 1 {
		index = 1
	}
	if index > len(values) {
		index = len(values)
	}

	return values[index-1]
}
