package handles_test

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/f-sync/fsync/internal/handles"
)

const (
	resolverTestIntentHTMLSuccess          = "<html><head><title>Example Name (@example) / X</title></head><body><a href=\"https://x.com/example\">profile</a></body></html>"
	resolverTestIntentHTMLMissingHandle    = "<html><head><title>Example Name (@example) / X</title></head><body>No links</body></html>"
	resolverTestIntentHTMLNoTitle          = "<html><body><a href=\"https://x.com/example\">profile</a></body></html>"
	resolverTestIntentURLPrefix            = "https://x.com/intent/user?user_id="
	resolverTestErrorMessageMissingAccount = "no stub intent response for account"

	resolverTestAccountIDSuccess           = "10001"
	resolverTestAccountIDMissingHandle     = "10002"
	resolverTestAccountIDNoTitle           = "10003"
	resolverTestAccountIDFetcherError      = "10004"
	resolverTestAccountIDPrimaryDedup      = "20001"
	resolverTestAccountIDSecondaryDedup    = "20002"
	resolverTestAccountIDCacheReuse        = "20003"
	resolverTestAccountIDSharedAcrossCache = "20004"

	resolverIntegrationFlagName                 = "twitter_integration"
	resolverIntegrationFlagDescription          = "enable live Twitter intent resolution integration test"
	resolverIntegrationFlagDisabledMessage      = "twitter integration test skipped because the flag is disabled"
	resolverIntegrationChromeUnavailableMessage = "twitter integration test skipped because no Chrome binary is available"
	resolverIntegrationTestCaseNameElon         = "elon musk intent resolution"
	resolverIntegrationAccountIDElon            = "44196397"
	resolverIntegrationExpectedUserNameElon     = "elonmusk"
	resolverIntegrationCreateResolverError      = "create integration resolver"
	resolverIntegrationResolveErrorFormat       = "integration resolver returned error: %v"
	resolverIntegrationUnexpectedHandleFormat   = "expected integration handle %s, got %s"
	resolverIntegrationEmptyDisplayNameMessage  = "expected integration display name to be non-empty"
	resolverIntegrationSkipMessageFormat        = "%s: %v"
	resolverIntegrationErrMessageEmptyChrome    = "resolved empty chrome binary path"
	resolverIntegrationContextTimeoutSeconds    = 30
)

var (
	resolverIntegrationRunFlag = flag.Bool(resolverIntegrationFlagName, false, resolverIntegrationFlagDescription)

	resolverIntegrationRequestTimeout = time.Duration(resolverIntegrationContextTimeoutSeconds) * time.Second
)

type recordingIntentFetcher struct {
	responses map[string]handles.IntentPage
	errors    map[string]error
	mu        sync.Mutex
	calls     map[string]int
}

func newRecordingIntentFetcher(responses map[string]handles.IntentPage, errors map[string]error) *recordingIntentFetcher {
	return &recordingIntentFetcher{
		responses: responses,
		errors:    errors,
		calls:     make(map[string]int),
	}
}

func resolverIntegrationChromeBinaryPath() (string, error) {
	resolvedPath := handles.ResolveChromeBinaryPath(handles.Config{})
	trimmedPath := strings.TrimSpace(resolvedPath)
	if trimmedPath == "" {
		return "", errors.New(resolverIntegrationErrMessageEmptyChrome)
	}
	if strings.ContainsRune(trimmedPath, os.PathSeparator) {
		if _, statErr := os.Stat(trimmedPath); statErr != nil {
			return "", statErr
		}
		return trimmedPath, nil
	}
	lookedPath, lookErr := exec.LookPath(trimmedPath)
	if lookErr != nil {
		return "", lookErr
	}
	return lookedPath, nil
}

func (fetcher *recordingIntentFetcher) FetchIntentPage(ctx context.Context, request handles.IntentRequest) (handles.IntentPage, error) {
	fetcher.mu.Lock()
	fetcher.calls[request.AccountID]++
	fetcher.mu.Unlock()

	if fetchErr, exists := fetcher.errors[request.AccountID]; exists {
		return handles.IntentPage{}, fetchErr
	}
	if response, exists := fetcher.responses[request.AccountID]; exists {
		return response, nil
	}
	return handles.IntentPage{}, fmt.Errorf("%s %s", resolverTestErrorMessageMissingAccount, request.AccountID)
}

func TestResolverResolveAccount(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name                string
		accountID           string
		htmlContent         string
		fetchError          error
		expectError         bool
		expectedUserName    string
		expectedDisplayName string
	}{
		{
			name:                "successful resolution",
			accountID:           resolverTestAccountIDSuccess,
			htmlContent:         resolverTestIntentHTMLSuccess,
			expectedUserName:    "example",
			expectedDisplayName: "Example Name",
		},
		{
			name:        "missing handle",
			accountID:   resolverTestAccountIDMissingHandle,
			htmlContent: resolverTestIntentHTMLMissingHandle,
			expectError: true,
		},
		{
			name:                "no title retains empty display name",
			accountID:           resolverTestAccountIDNoTitle,
			htmlContent:         resolverTestIntentHTMLNoTitle,
			expectedUserName:    "example",
			expectedDisplayName: "",
		},
		{
			name:        "fetcher returns error",
			accountID:   resolverTestAccountIDFetcherError,
			fetchError:  fmt.Errorf("fetch failure"),
			expectError: true,
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			responses := make(map[string]handles.IntentPage)
			errors := make(map[string]error)
			if testCase.htmlContent != "" {
				responses[testCase.accountID] = handles.IntentPage{HTML: testCase.htmlContent, SourceURL: resolverTestIntentURLPrefix + testCase.accountID}
			}
			if testCase.fetchError != nil {
				errors[testCase.accountID] = testCase.fetchError
			}
			fetcher := newRecordingIntentFetcher(responses, errors)
			resolver, err := handles.NewResolver(handles.Config{IntentFetcher: fetcher, MaxConcurrent: 2})
			if err != nil {
				t.Fatalf("create resolver: %v", err)
			}

			record, resolveErr := resolver.ResolveAccount(context.Background(), testCase.accountID)
			if testCase.expectError {
				if resolveErr == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if resolveErr != nil {
				t.Fatalf("unexpected error: %v", resolveErr)
			}
			if record.UserName != testCase.expectedUserName {
				t.Fatalf("unexpected username: %s", record.UserName)
			}
			if record.DisplayName != testCase.expectedDisplayName {
				t.Fatalf("unexpected display name: %s", record.DisplayName)
			}
			if record.AccountID != testCase.accountID {
				t.Fatalf("unexpected account id: %s", record.AccountID)
			}
		})
	}
}

func TestResolverResolveManyDeduplicates(t *testing.T) {
	accountResponses := map[string]handles.IntentPage{
		resolverTestAccountIDPrimaryDedup: {
			HTML:      resolverTestIntentHTMLSuccess,
			SourceURL: resolverTestIntentURLPrefix + resolverTestAccountIDPrimaryDedup,
		},
		resolverTestAccountIDSecondaryDedup: {
			HTML:      strings.ReplaceAll(resolverTestIntentHTMLSuccess, "example", "second"),
			SourceURL: resolverTestIntentURLPrefix + resolverTestAccountIDSecondaryDedup,
		},
	}
	fetcher := newRecordingIntentFetcher(accountResponses, nil)
	resolver, err := handles.NewResolver(handles.Config{IntentFetcher: fetcher, MaxConcurrent: 3})
	if err != nil {
		t.Fatalf("create resolver: %v", err)
	}

	accountIDs := []string{
		resolverTestAccountIDPrimaryDedup,
		resolverTestAccountIDPrimaryDedup,
		resolverTestAccountIDSecondaryDedup,
		"",
		resolverTestAccountIDSecondaryDedup,
	}
	results := resolver.ResolveMany(context.Background(), accountIDs)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if fetcher.calls[resolverTestAccountIDPrimaryDedup] != 1 {
		t.Fatalf("expected single fetch for account %s, got %d", resolverTestAccountIDPrimaryDedup, fetcher.calls[resolverTestAccountIDPrimaryDedup])
	}
	if fetcher.calls[resolverTestAccountIDSecondaryDedup] != 1 {
		t.Fatalf("expected single fetch for account %s, got %d", resolverTestAccountIDSecondaryDedup, fetcher.calls[resolverTestAccountIDSecondaryDedup])
	}
}

func TestResolverCachesResults(t *testing.T) {
	fetcher := newRecordingIntentFetcher(map[string]handles.IntentPage{
		resolverTestAccountIDCacheReuse: {
			HTML:      resolverTestIntentHTMLSuccess,
			SourceURL: resolverTestIntentURLPrefix + resolverTestAccountIDCacheReuse,
		},
	}, nil)
	resolver, err := handles.NewResolver(handles.Config{IntentFetcher: fetcher, MaxConcurrent: 2})
	if err != nil {
		t.Fatalf("create resolver: %v", err)
	}

	_, firstErr := resolver.ResolveAccount(context.Background(), resolverTestAccountIDCacheReuse)
	if firstErr != nil {
		t.Fatalf("first resolution failed: %v", firstErr)
	}
	_, secondErr := resolver.ResolveAccount(context.Background(), resolverTestAccountIDCacheReuse)
	if secondErr != nil {
		t.Fatalf("second resolution failed: %v", secondErr)
	}
	if fetcher.calls[resolverTestAccountIDCacheReuse] != 1 {
		t.Fatalf("expected cached response to avoid duplicate fetch, got %d calls", fetcher.calls[resolverTestAccountIDCacheReuse])
	}
}

func TestResolverSharesCacheAcrossInstances(t *testing.T) {
	testCases := []struct {
		name string
	}{
		{
			name: "shared cache prevents duplicate fetches",
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			firstFetcher := newRecordingIntentFetcher(map[string]handles.IntentPage{
				resolverTestAccountIDSharedAcrossCache: {
					HTML:      resolverTestIntentHTMLSuccess,
					SourceURL: resolverTestIntentURLPrefix + resolverTestAccountIDSharedAcrossCache,
				},
			}, nil)
			firstResolver, err := handles.NewResolver(handles.Config{IntentFetcher: firstFetcher, MaxConcurrent: 2})
			if err != nil {
				t.Fatalf("create first resolver: %v", err)
			}

			firstRecord, firstErr := firstResolver.ResolveAccount(context.Background(), resolverTestAccountIDSharedAcrossCache)
			if firstErr != nil {
				t.Fatalf("first resolver failed: %v", firstErr)
			}

			secondFetcher := newRecordingIntentFetcher(map[string]handles.IntentPage{}, nil)
			secondResolver, err := handles.NewResolver(handles.Config{IntentFetcher: secondFetcher, MaxConcurrent: 2})
			if err != nil {
				t.Fatalf("create second resolver: %v", err)
			}

			secondRecord, secondErr := secondResolver.ResolveAccount(context.Background(), resolverTestAccountIDSharedAcrossCache)
			if secondErr != nil {
				t.Fatalf("second resolver returned error: %v", secondErr)
			}
			if secondFetcher.calls[resolverTestAccountIDSharedAcrossCache] != 0 {
				t.Fatalf("expected shared cache to prevent second fetch, observed %d calls", secondFetcher.calls[resolverTestAccountIDSharedAcrossCache])
			}
			if secondRecord.UserName != firstRecord.UserName {
				t.Fatalf("expected cached username %s, got %s", firstRecord.UserName, secondRecord.UserName)
			}
			if secondRecord.DisplayName != firstRecord.DisplayName {
				t.Fatalf("expected cached display name %s, got %s", firstRecord.DisplayName, secondRecord.DisplayName)
			}
		})
	}
}

func TestResolverIntegrationResolveElonMusk(t *testing.T) {
	if !*resolverIntegrationRunFlag {
		t.Skip(resolverIntegrationFlagDisabledMessage)
	}

	chromeBinaryPath, chromeErr := resolverIntegrationChromeBinaryPath()
	if chromeErr != nil {
		t.Skipf(resolverIntegrationSkipMessageFormat, resolverIntegrationChromeUnavailableMessage, chromeErr)
	}

	resolver, err := handles.NewResolver(handles.Config{ChromeBinaryPath: chromeBinaryPath})
	if err != nil {
		t.Fatalf(resolverIntegrationSkipMessageFormat, resolverIntegrationCreateResolverError, err)
	}

	testCases := []struct {
		name             string
		accountID        string
		expectedUserName string
	}{
		{
			name:             resolverIntegrationTestCaseNameElon,
			accountID:        resolverIntegrationAccountIDElon,
			expectedUserName: resolverIntegrationExpectedUserNameElon,
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), resolverIntegrationRequestTimeout)
			defer cancel()

			record, resolveErr := resolver.ResolveAccount(ctx, testCase.accountID)
			if resolveErr != nil {
				if errors.Is(resolveErr, exec.ErrNotFound) {
					t.Skipf(resolverIntegrationSkipMessageFormat, resolverIntegrationChromeUnavailableMessage, resolveErr)
				}
				t.Fatalf(resolverIntegrationResolveErrorFormat, resolveErr)
			}
			if record.UserName != testCase.expectedUserName {
				t.Fatalf(resolverIntegrationUnexpectedHandleFormat, testCase.expectedUserName, record.UserName)
			}
			if strings.TrimSpace(record.DisplayName) == "" {
				t.Fatalf(resolverIntegrationEmptyDisplayNameMessage)
			}
		})
	}
}
