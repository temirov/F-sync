package handles_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/f-sync/fsync/internal/handles"
)

const (
	argumentPrinterScriptName        = "print_args.sh"
	argumentPrinterScriptContent     = "#!/bin/sh\nfor argument in \"$@\"; do\n        printf '%s\\n' \"$argument\"\ndone\n"
	chromeUserAgentArgumentPrefix    = "--user-agent="
	fetcherTestIntentAccountID       = "40001"
	fetcherTestIntentURLTemplate     = "https://x.com/intent/user?user_id=%s"
	fetcherTestCustomUserAgentString = "CustomAgent/200.0"
)

func TestChromeIntentFetcherUserAgentSelection(t *testing.T) {
	t.Parallel()

	argumentPrinterPath := createArgumentPrinterScript(t)
	allowedAgents := handles.DefaultChromeUserAgents()
	if len(allowedAgents) == 0 {
		t.Fatalf("expected modern user agent list to be non-empty")
	}

	testCases := []struct {
		name                string
		configuredUserAgent string
		expectExactMatch    bool
	}{
		{
			name:                "defaults to modern user agent",
			configuredUserAgent: "",
		},
		{
			name:                "uses explicit user agent",
			configuredUserAgent: fetcherTestCustomUserAgentString,
			expectExactMatch:    true,
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			fetcher, fetcherErr := handles.NewChromeIntentFetcher(handles.ChromeFetcherConfig{
				BinaryPath:        argumentPrinterPath,
				UserAgent:         testCase.configuredUserAgent,
				VirtualTimeBudget: 0,
				RequestDelay:      -1,
			})
			if fetcherErr != nil {
				t.Fatalf("create fetcher: %v", fetcherErr)
			}

			intentURL := fmt.Sprintf(fetcherTestIntentURLTemplate, fetcherTestIntentAccountID)
			intentRequest := handles.IntentRequest{AccountID: fetcherTestIntentAccountID, URL: intentURL}

			intentPage, fetchErr := fetcher.FetchIntentPage(context.Background(), intentRequest)
			if fetchErr != nil {
				t.Fatalf("fetch intent page: %v", fetchErr)
			}

			userAgentArgument := extractUserAgentArgument(intentPage.HTML)
			if userAgentArgument == "" {
				t.Fatalf("user agent argument not present in command output: %s", intentPage.HTML)
			}
			actualUserAgent := strings.TrimPrefix(userAgentArgument, chromeUserAgentArgumentPrefix)

			if testCase.expectExactMatch {
				if actualUserAgent != testCase.configuredUserAgent {
					t.Fatalf("expected explicit user agent %s, received %s", testCase.configuredUserAgent, actualUserAgent)
				}
				return
			}

			if !agentInSet(actualUserAgent, allowedAgents) {
				t.Fatalf("expected modern user agent, received %s", actualUserAgent)
			}
		})
	}
}

func createArgumentPrinterScript(t *testing.T) string {
	t.Helper()
	temporaryDirectory := t.TempDir()
	scriptPath := filepath.Join(temporaryDirectory, argumentPrinterScriptName)
	if writeErr := os.WriteFile(scriptPath, []byte(argumentPrinterScriptContent), 0o755); writeErr != nil {
		t.Fatalf("write argument printer script: %v", writeErr)
	}
	if chmodErr := os.Chmod(scriptPath, 0o755); chmodErr != nil {
		t.Fatalf("set script executable: %v", chmodErr)
	}
	return scriptPath
}

func extractUserAgentArgument(output string) string {
	outputLines := strings.Split(strings.TrimSpace(output), "\n")
	for _, line := range outputLines {
		trimmedLine := strings.TrimSpace(line)
		if strings.HasPrefix(trimmedLine, chromeUserAgentArgumentPrefix) {
			return trimmedLine
		}
	}
	return ""
}

func agentInSet(candidate string, agents []string) bool {
	for _, agent := range agents {
		if candidate == agent {
			return true
		}
	}
	return false
}
