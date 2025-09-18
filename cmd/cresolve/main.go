// cmd/cresolve/main.go
package main

import (
	"bytes"
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
	// EXACTLY like: grep -Eo 'https://(x|twitter)\.com/[A-Za-z0-9_]{1,15}' (HTTPS only)
	profileURLRegex = regexp.MustCompile(`https://(?:x|twitter)\.com/[A-Za-z0-9_]{1,15}`)

	// Same reserved filter as your shell
	reservedTopLevelPaths = map[string]struct{}{
		"i": {}, "intent": {}, "home": {}, "tos": {}, "privacy": {}, "explore": {},
		"notifications": {}, "settings": {}, "login": {}, "signup": {}, "share": {},
		"account": {}, "compose": {}, "messages": {}, "search": {},
	}
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: cresolve <numeric_id>")
		os.Exit(2)
	}
	numericIdentifier := strings.TrimSpace(os.Args[1])
	if numericIdentifier == "" {
		fmt.Fprintln(os.Stderr, "numeric_id must be non-empty")
		os.Exit(2)
	}

	chromeBinaryPath := os.Getenv("CHROME_BIN")
	if chromeBinaryPath == "" {
		chromeBinaryPath = defaultChromeBinaryPath
	}

	targetPageURL := fmt.Sprintf(intentUserURLTemplate, numericIdentifier)

	htmlDocument, execError := renderWithHeadlessChrome(chromeBinaryPath, targetPageURL)
	if execError != nil || strings.TrimSpace(htmlDocument) == "" {
		fmt.Fprintf(os.Stderr, "unresolvable or login-walled id: %s\n", numericIdentifier)
		os.Exit(1)
	}

	// tr "'" '"'
	normalizedHTML := strings.ReplaceAll(htmlDocument, `'`, `"`)

	// grep -Eo ...
	matches := profileURLRegex.FindAllString(normalizedHTML, -1)
	for _, matchedURL := range matches {
		// sed -E 's#https?://(x|twitter)\.com/##'  (HTTPS-only)
		handle := stripDomainPrefix(matchedURL)

		// grep -Ev '^(reserved...)$'
		if isReserved(handle) {
			continue
		}

		// head -n1 (first acceptable match)
		fmt.Printf("%s (retrieved from %s)\n", handle, targetPageURL)
		return
	}

	fmt.Fprintf(os.Stderr, "unresolvable or login-walled id: %s\n", numericIdentifier)
	os.Exit(1)
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
		"--user-agent=" + explicitUserAgentString, // enforce UA like a normal browser
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
	// Equivalent to: sed -E 's#https?://(x|twitter)\.com/##'
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
