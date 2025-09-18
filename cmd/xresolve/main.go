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
	"io"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/f-sync/fsync/internal/intentparser"
)

type resolveJob struct {
	NumericID string
	Index     int
}

type resolveResult struct {
	NumericID     string
	Handle        string
	DisplayName   string
	ResolvedAtUTC string
	Source        string
	ErrorMessage  string
	Index         int
}

const (
	redirectBaseURL                 = "https://x.com/i/user/"
	intentURLFormat                 = "https://x.com/intent/user?user_id=%s"
	locationHeaderName              = "Location"
	retryAfterHeaderName            = "Retry-After"
	redirectSourceName              = "redirect"
	intentSourceName                = "intent"
	missingLocationHeaderMessage    = "no Location header"
	parseLocationErrorFormat        = "parse Location: %v"
	emptyHandleLocationMessage      = "empty handle in Location"
	intentStatusErrorFormat         = "intent status: %s"
	intentReadBodyErrorFormat       = "read intent body: %v"
	intentRequestBuildErrorFormat   = "build intent request: %v"
	redirectRequestBuildErrorFormat = "build redirect request: %v"
	emptyIntentBodyMessage          = "intent body empty"
	latestResolvedMessageFormat     = "Latest resolved: %s"
	wroteOutputWithPreviewFormat    = "Wrote %s (%d rows). %s\n"
	wroteOutputFormat               = "Wrote %s (%d rows)\n"
	displayHandleFormat             = "%s (@%s)"
	handleOnlyFormat                = "@%s"
	csvColumnID                     = "id"
	csvColumnHandle                 = "handle"
	csvColumnDisplayName            = "display_name"
	csvColumnResolvedAt             = "resolved_at_utc"
	csvColumnSource                 = "source"
	csvColumnError                  = "error"
)

var csvHeaderColumns = []string{csvColumnID, csvColumnHandle, csvColumnDisplayName, csvColumnResolvedAt, csvColumnSource, csvColumnError}

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
	summaryMessage := latestResolvedSummary(results)
	if summaryMessage != "" {
		fmt.Printf(wroteOutputWithPreviewFormat, *outputPath, len(results), summaryMessage)
	} else {
		fmt.Printf(wroteOutputFormat, *outputPath, len(results))
	}
}

func resolveOne(httpClient *http.Client, numericID string) resolveResult {
	resolvedAt := time.Now().UTC().Format(time.RFC3339)
	result := resolveResult{NumericID: numericID, ResolvedAtUTC: resolvedAt}

	redirectHandle, redirectErr := fetchRedirectHandle(httpClient, numericID)
	if redirectErr != nil {
		result.Source = redirectSourceName
		result.ErrorMessage = redirectErr.Error()
	} else if redirectHandle != "" {
		result.Handle = redirectHandle
		result.Source = redirectSourceName
		result.ErrorMessage = ""
	}

	intentHTML, intentErr := fetchIntentHTML(httpClient, numericID)
	if intentErr != nil {
		if result.Handle == "" {
			result.Source = intentSourceName
		}
		result.ErrorMessage = intentErr.Error()
		return result
	}

	htmlParser := intentparser.NewIntentHTMLParser(intentHTML)
	parsedHandle, parseErr := htmlParser.ExtractHandle()
	if parseErr != nil {
		if result.Handle == "" {
			result.Source = intentSourceName
		}
		result.ErrorMessage = parseErr.Error()
		return result
	}

	result.Handle = parsedHandle
	result.DisplayName = htmlParser.ExtractDisplayName(parsedHandle)
	result.Source = intentSourceName
	result.ErrorMessage = ""
	return result
}

func fetchRedirectHandle(httpClient *http.Client, numericID string) (string, error) {
	requestURL := redirectBaseURL + numericID
	httpRequest, requestErr := http.NewRequest(http.MethodGet, requestURL, nil)
	if requestErr != nil {
		return "", fmt.Errorf(redirectRequestBuildErrorFormat, requestErr)
	}
	httpResponse, httpErr := httpClient.Do(httpRequest)
	if httpErr != nil {
		return "", httpErr
	}
	if httpResponse.StatusCode == http.StatusTooManyRequests {
		retryAfter := httpResponse.Header.Get(retryAfterHeaderName)
		httpResponse.Body.Close()
		time.Sleep(backoffFromRetryAfter(retryAfter))
		return fetchRedirectHandle(httpClient, numericID)
	}
	defer httpResponse.Body.Close()

	locationHeader := httpResponse.Header.Get(locationHeaderName)
	if locationHeader == "" {
		return "", fmt.Errorf(missingLocationHeaderMessage)
	}
	parsedLocation, parseErr := url.Parse(locationHeader)
	if parseErr != nil {
		return "", fmt.Errorf(parseLocationErrorFormat, parseErr)
	}
	cleanedPath := strings.Trim(parsedLocation.Path, "/")
	pathSegments := strings.Split(cleanedPath, "/")
	if len(pathSegments) == 0 || strings.TrimSpace(pathSegments[0]) == "" {
		return "", fmt.Errorf(emptyHandleLocationMessage)
	}
	return pathSegments[0], nil
}

func fetchIntentHTML(httpClient *http.Client, numericID string) (string, error) {
	requestURL := fmt.Sprintf(intentURLFormat, numericID)
	httpRequest, requestErr := http.NewRequest(http.MethodGet, requestURL, nil)
	if requestErr != nil {
		return "", fmt.Errorf(intentRequestBuildErrorFormat, requestErr)
	}
	httpResponse, httpErr := httpClient.Do(httpRequest)
	if httpErr != nil {
		return "", httpErr
	}
	if httpResponse.StatusCode == http.StatusTooManyRequests {
		retryAfter := httpResponse.Header.Get(retryAfterHeaderName)
		httpResponse.Body.Close()
		time.Sleep(backoffFromRetryAfter(retryAfter))
		return fetchIntentHTML(httpClient, numericID)
	}
	defer httpResponse.Body.Close()
	if httpResponse.StatusCode >= http.StatusBadRequest {
		return "", fmt.Errorf(intentStatusErrorFormat, httpResponse.Status)
	}
	bodyBytes, readErr := io.ReadAll(httpResponse.Body)
	if readErr != nil {
		return "", fmt.Errorf(intentReadBodyErrorFormat, readErr)
	}
	htmlContent := string(bodyBytes)
	if strings.TrimSpace(htmlContent) == "" {
		return "", fmt.Errorf(emptyIntentBodyMessage)
	}
	return htmlContent, nil
}

func latestResolvedSummary(results []resolveResult) string {
	for index := len(results) - 1; index >= 0; index-- {
		label := formatResultLabel(results[index])
		if label != "" {
			return fmt.Sprintf(latestResolvedMessageFormat, label)
		}
	}
	return ""
}

func formatResultLabel(result resolveResult) string {
	displayName := strings.TrimSpace(result.DisplayName)
	handle := strings.TrimSpace(result.Handle)
	switch {
	case displayName != "" && handle != "":
		return fmt.Sprintf(displayHandleFormat, displayName, handle)
	case handle != "":
		return fmt.Sprintf(handleOnlyFormat, handle)
	case strings.TrimSpace(result.NumericID) != "":
		return strings.TrimSpace(result.NumericID)
	default:
		return ""
	}
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

	if err := w.Write(csvHeaderColumns); err != nil {
		return err
	}
	for _, resolveOutcome := range results {
		record := []string{resolveOutcome.NumericID, resolveOutcome.Handle, resolveOutcome.DisplayName, resolveOutcome.ResolvedAtUTC, resolveOutcome.Source, resolveOutcome.ErrorMessage}
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
