// Command xresolve: resolve X/Twitter numeric IDs to handles by reading the redirect Location.
// Usage:
//
//	xresolve --in ids.txt --out id_to_handle.csv --concurrency 8 --sleep-ms 125
//
// ids.txt: one numeric ID per line
package main

import (
	"bufio"
	"encoding/csv"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

type resolveJob struct {
	NumericID string
	Index     int
}

type resolveResult struct {
	NumericID     string
	Handle        string
	ResolvedAtUTC string
	Source        string
	ErrorMessage  string
	Index         int
}

func main() {
	inputPath := flag.String("in", "", "Path to input file with one numeric ID per line")
	outputPath := flag.String("out", "id_to_handle.csv", "Output CSV path")
	concurrency := flag.Int("concurrency", max(2, runtime.NumCPU()), "Concurrent workers")
	sleepMillis := flag.Int("sleep-ms", 125, "Inter-request sleep (milliseconds) per worker")
	flag.Parse()

	if *inputPath == "" {
		fmt.Fprintln(os.Stderr, "missing --in file")
		os.Exit(2)
	}

	numericIDs, readErr := readIDs(*inputPath)
	if readErr != nil {
		fmt.Fprintln(os.Stderr, readErr)
		os.Exit(1)
	}
	if len(numericIDs) == 0 {
		fmt.Fprintln(os.Stderr, "no IDs to resolve")
		os.Exit(0)
	}

	httpClient := &http.Client{
		Timeout: 12 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse // we want Location, do not follow
		},
	}

	jobChannel := make(chan resolveJob)
	resultChannel := make(chan resolveResult, len(numericIDs))
	var waitGroup sync.WaitGroup

	for workerIndex := 0; workerIndex < *concurrency; workerIndex++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			for job := range jobChannel {
				result := resolveOne(httpClient, job.NumericID)
				result.Index = job.Index
				resultChannel <- result
				time.Sleep(time.Duration(*sleepMillis) * time.Millisecond)
			}
		}()
	}

	for idx, id := range numericIDs {
		jobChannel <- resolveJob{NumericID: id, Index: idx}
	}
	close(jobChannel)

	go func() {
		waitGroup.Wait()
		close(resultChannel)
	}()

	results := make([]resolveResult, len(numericIDs))
	for r := range resultChannel {
		results[r.Index] = r
	}

	if writeErr := writeCSV(*outputPath, results); writeErr != nil {
		fmt.Fprintln(os.Stderr, writeErr)
		os.Exit(1)
	}
	fmt.Printf("Wrote %s (%d rows)\n", *outputPath, len(results))
}

func resolveOne(httpClient *http.Client, numericID string) resolveResult {
	const base = "https://x.com/i/user/"
	requestURL := base + numericID

	req, _ := http.NewRequest(http.MethodGet, requestURL, nil)
	resp, err := httpClient.Do(req)
	now := time.Now().UTC().Format(time.RFC3339)

	if err != nil {
		return resolveResult{NumericID: numericID, ResolvedAtUTC: now, Source: "redirect", ErrorMessage: err.Error()}
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		retryAfter := resp.Header.Get("Retry-After")
		sleepDuration := backoffFromRetryAfter(retryAfter)
		time.Sleep(sleepDuration)
		return resolveOne(httpClient, numericID)
	}

	location := resp.Header.Get("Location")
	if location == "" {
		return resolveResult{NumericID: numericID, ResolvedAtUTC: now, Source: "redirect", ErrorMessage: "no Location header"}
	}

	parsed, parseErr := url.Parse(location)
	if parseErr != nil {
		return resolveResult{NumericID: numericID, ResolvedAtUTC: now, Source: "redirect", ErrorMessage: "parse Location: " + parseErr.Error()}
	}

	cleanPath := strings.Trim(parsed.Path, "/")
	segments := strings.Split(cleanPath, "/")
	if len(segments) == 0 || segments[0] == "" {
		return resolveResult{NumericID: numericID, ResolvedAtUTC: now, Source: "redirect", ErrorMessage: "empty handle in Location"}
	}
	handle := segments[0]
	return resolveResult{NumericID: numericID, Handle: handle, ResolvedAtUTC: now, Source: "redirect"}
}

func readIDs(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var ids []string
	sc := bufio.NewScanner(file)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if !isAllDigits(line) {
			continue
		}
		ids = append(ids, line)
	}
	return ids, sc.Err()
}

func writeCSV(path string, results []resolveResult) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	w := csv.NewWriter(file)
	defer w.Flush()

	header := []string{"id", "handle", "resolved_at_utc", "source", "error"}
	if err := w.Write(header); err != nil {
		return err
	}
	for _, r := range results {
		record := []string{r.NumericID, r.Handle, r.ResolvedAtUTC, r.Source, r.ErrorMessage}
		if err := w.Write(record); err != nil {
			return err
		}
	}
	return w.Error()
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func backoffFromRetryAfter(retryAfter string) time.Duration {
	if retryAfter == "" {
		return 30 * time.Second
	}
	// Header may be seconds or HTTP-date; we handle seconds here.
	if secs, err := strconv.Atoi(retryAfter); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	return 30 * time.Second
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
