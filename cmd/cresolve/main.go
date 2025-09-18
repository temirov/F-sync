// cmd/cresolve/main.go
package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

const (
	defaultChromeBinaryPath             = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
	virtualTimeBudgetMillisecondsString = "15000"
	renderTimeoutSeconds                = 30
	explicitUserAgentString             = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/118.0.0.0 Safari/537.36"
	intentUserURLTemplate               = "https://x.com/intent/user?user_id=%s"
)

var (
	// HTTPS-only, exactly matching: grep -Eo 'https://(x|twitter)\.com/[A-Za-z0-9_]{1,15}'
	profileURLRegex = regexp.MustCompile(`https://(?:x|twitter)\.com/[A-Za-z0-9_]{1,15}`)

	// Reserved top-level paths to exclude
	reservedTopLevelPaths = map[string]struct{}{
		"i": {}, "intent": {}, "home": {}, "tos": {}, "privacy": {}, "explore": {},
		"notifications": {}, "settings": {}, "login": {}, "signup": {}, "share": {},
		"account": {}, "compose": {}, "messages": {}, "search": {},
	}
)

func main() {
	outputCSV := flag.Bool("csv", false, "output CSV: id,handle")
	flag.Parse()

	chromeBinaryPath := os.Getenv("CHROME_BIN")
	if chromeBinaryPath == "" {
		chromeBinaryPath = defaultChromeBinaryPath
	}

	var identifiers []string
	if flag.NArg() > 0 {
		for _, arg := range flag.Args() {
			trimmed := strings.TrimSpace(arg)
			if trimmed != "" {
				identifiers = append(identifiers, trimmed)
			}
		}
	} else {
		inputScanner := bufio.NewScanner(os.Stdin)
		for inputScanner.Scan() {
			trimmed := strings.TrimSpace(inputScanner.Text())
			if trimmed != "" {
				identifiers = append(identifiers, trimmed)
			}
		}
		if err := inputScanner.Err(); err != nil {
			fmt.Fprintln(os.Stderr, "stdin read error:", err)
			os.Exit(2)
		}
	}

	// If still nothing, treat as single-id usage error (keeps old UX sane)
	if len(identifiers) == 0 {
		fmt.Fprintln(os.Stderr, "usage:")
		fmt.Fprintln(os.Stderr, "  cresolve <numeric_id>")
		fmt.Fprintln(os.Stderr, "  cresolve -csv <id1> <id2> ...")
		fmt.Fprintln(os.Stderr, "  cat ids.txt | cresolve -csv")
		os.Exit(2)
	}

	for _, numericIdentifier := range identifiers {
		targetPageURL := fmt.Sprintf(intentUserURLTemplate, numericIdentifier)

		renderedHTML, execError := renderWithHeadlessChrome(chromeBinaryPath, targetPageURL)
		var resolvedHandle string
		if execError == nil && strings.TrimSpace(renderedHTML) != "" {
			normalizedHTML := strings.ReplaceAll(renderedHTML, `'`, `"`)
			matches := profileURLRegex.FindAllString(normalizedHTML, -1)
			for _, matchedURL := range matches {
				candidate := stripDomainPrefix(matchedURL)
				if isReserved(candidate) {
					continue
				}
				resolvedHandle = candidate
				break // equivalent to head -n1
			}
		}

		if *outputCSV {
			// CSV: id,handle (blank handle on failure)
			fmt.Printf("%s,%s\n", numericIdentifier, resolvedHandle)
		} else {
			// Pretty: id: handle (retrieved from URL) or id:
			if resolvedHandle != "" {
				fmt.Printf("%s: %s (retrieved from %s)\n", numericIdentifier, resolvedHandle, targetPageURL)
			} else {
				fmt.Printf("%s:\n", numericIdentifier)
			}
		}
	}
}

func renderWithHeadlessChrome(chromeBinaryPath, targetPageURL string) (string, error) {
	execArguments := []string{
		"--headless=new",
		"--disable-gpu",
		"--use-gl=swiftshader",
		"--enable-unsafe-swiftshader",
		"--hide-scrollbars",
		"--no-first-run",
		"--no-default-browser-check",
		"--log-level=3",
		"--silent",
		"--disable-logging",
		"--user-agent=" + explicitUserAgentString, // enforce UA
		"--virtual-time-budget=" + virtualTimeBudgetMillisecondsString,
		"--dump-dom",
		targetPageURL,
	}
	execCommand := exec.Command(chromeBinaryPath, execArguments...)
	var stdoutBuffer bytes.Buffer
	execCommand.Stdout = &stdoutBuffer
	execCommand.Stderr = io.Discard // silence Chrome noise

	if startError := execCommand.Start(); startError != nil {
		return "", startError
	}

	doneChannel := make(chan error, 1)
	go func() { doneChannel <- execCommand.Wait() }()

	select {
	case waitError := <-doneChannel:
		if waitError != nil {
			return "", waitError
		}
	case <-time.After(renderTimeoutSeconds * time.Second):
		_ = execCommand.Process.Kill()
		return "", fmt.Errorf("chrome render timeout")
	}

	return stdoutBuffer.String(), nil
}

func stripDomainPrefix(fullURL string) string {
	// Equivalent to: sed -E 's#https?://(x|twitter)\.com/##' (HTTPS-only here)
	fullURL = strings.TrimPrefix(fullURL, "https://")
	if slashIndex := strings.IndexByte(fullURL, '/'); slashIndex >= 0 {
		return fullURL[slashIndex+1:] // handle
	}
	return fullURL
}

func isReserved(handle string) bool {
	_, isBad := reservedTopLevelPaths[strings.ToLower(handle)]
	return isBad
}
