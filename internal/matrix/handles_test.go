package matrix_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/f-sync/fsync/internal/handles"
	"github.com/f-sync/fsync/internal/matrix"
)

const (
	stubIntentHTMLSuccess             = "<html><head><title>Resolved Name (@resolved) / X</title></head><body><a href=\"https://x.com/resolved\">profile</a></body></html>"
	stubIntentHTMLMissingHandle       = "<html><head><title>Resolved Name (@resolved) / X</title></head><body>No links</body></html>"
	stubIntentSourceURLPrefix         = "https://x.com/intent/user?user_id="
	stubIntentErrorMessageMissing     = "no stub intent page for account"
	matrixTestAccountIDDisabled       = "31001"
	matrixTestAccountIDSuccess        = "31002"
	matrixTestAccountIDMissingHandle  = "31003"
	matrixTestAccountIDFetcherFailure = "31004"
)

type stubIntentFetcher struct {
	htmlByAccountID  map[string]string
	errorByAccountID map[string]error
	callCount        atomic.Int32
}

func (fetcher *stubIntentFetcher) FetchIntentPage(ctx context.Context, request handles.IntentRequest) (handles.IntentPage, error) {
	fetcher.callCount.Add(1)
	if fetchErr, exists := fetcher.errorByAccountID[request.AccountID]; exists {
		return handles.IntentPage{}, fetchErr
	}
	htmlContent, exists := fetcher.htmlByAccountID[request.AccountID]
	if !exists {
		return handles.IntentPage{}, fmt.Errorf("%s %s", stubIntentErrorMessageMissing, request.AccountID)
	}
	return handles.IntentPage{HTML: htmlContent, SourceURL: stubIntentSourceURLPrefix + request.AccountID}, nil
}

type stubResolver struct {
	callCount atomic.Int32
}

func (resolver *stubResolver) ResolveMany(_ context.Context, _ []string) map[string]handles.Result {
	resolver.callCount.Add(1)
	return nil
}

func TestMaybeResolveHandlesDisabled(t *testing.T) {
	decoratedRecord := matrix.AccountRecord{AccountID: matrixTestAccountIDDisabled}
	baseAccountSets := matrix.AccountSets{Followers: map[string]matrix.AccountRecord{matrixTestAccountIDDisabled: decoratedRecord}, Following: map[string]matrix.AccountRecord{matrixTestAccountIDDisabled: decoratedRecord}}

	testCases := []struct {
		name          string
		resolver      matrix.AccountHandleResolver
		shouldResolve bool
		expectedCalls int32
	}{
		{
			name:          "resolution disabled via flag",
			resolver:      &stubResolver{},
			shouldResolve: false,
			expectedCalls: 0,
		},
		{
			name:          "resolution disabled without resolver",
			resolver:      nil,
			shouldResolve: true,
			expectedCalls: 0,
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			followerSet := copyAccountSets(baseAccountSets)
			var callCounter *atomic.Int32
			if stub, ok := testCase.resolver.(*stubResolver); ok {
				callCounter = &stub.callCount
			}

			result := matrix.MaybeResolveHandles(context.Background(), testCase.resolver, testCase.shouldResolve, &followerSet)
			if result != nil {
				t.Fatalf("expected nil result, got %v", result)
			}
			if callCounter != nil && callCounter.Load() != testCase.expectedCalls {
				t.Fatalf("unexpected resolver call count: %d", callCounter.Load())
			}
			if followerSet.Followers[matrixTestAccountIDDisabled].UserName != "" {
				t.Fatalf("expected follower username to remain empty")
			}
		})
	}
}

func TestMaybeResolveHandlesResolution(t *testing.T) {
	testCases := []struct {
		name                string
		accountID           string
		htmlContent         string
		fetchError          error
		expectError         bool
		expectedUserName    string
		expectedDisplayName string
		expectedCalls       int32
	}{
		{
			name:                "successful resolution",
			accountID:           matrixTestAccountIDSuccess,
			htmlContent:         stubIntentHTMLSuccess,
			expectedUserName:    "resolved",
			expectedDisplayName: "Resolved Name",
			expectedCalls:       1,
		},
		{
			name:          "missing handle in html",
			accountID:     matrixTestAccountIDMissingHandle,
			htmlContent:   stubIntentHTMLMissingHandle,
			expectError:   true,
			expectedCalls: 1,
		},
		{
			name:          "fetcher error",
			accountID:     matrixTestAccountIDFetcherFailure,
			fetchError:    errors.New("fetch failed"),
			expectError:   true,
			expectedCalls: 1,
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			htmlResponses := make(map[string]string)
			errorResponses := make(map[string]error)
			if testCase.htmlContent != "" {
				htmlResponses[testCase.accountID] = testCase.htmlContent
			}
			if testCase.fetchError != nil {
				errorResponses[testCase.accountID] = testCase.fetchError
			}
			fetcher := &stubIntentFetcher{htmlByAccountID: htmlResponses, errorByAccountID: errorResponses}

			resolver, err := handles.NewResolver(handles.Config{
				IntentFetcher: fetcher,
				MaxConcurrent: 2,
			})
			if err != nil {
				t.Fatalf("create resolver: %v", err)
			}

			decoratedRecord := matrix.AccountRecord{AccountID: testCase.accountID}
			baseAccountSets := matrix.AccountSets{
				Followers: map[string]matrix.AccountRecord{testCase.accountID: decoratedRecord},
				Following: map[string]matrix.AccountRecord{testCase.accountID: decoratedRecord},
			}

			followerSet := copyAccountSets(baseAccountSets)
			result := matrix.MaybeResolveHandles(context.Background(), resolver, true, &followerSet)

			if testCase.expectError {
				if len(result) == 0 {
					t.Fatalf("expected errors from resolution")
				}
				if followerSet.Followers[testCase.accountID].UserName != "" {
					t.Fatalf("expected username to remain empty after failure")
				}
			} else {
				if len(result) != 0 {
					t.Fatalf("expected no errors, received %v", result)
				}
				if followerSet.Followers[testCase.accountID].UserName != testCase.expectedUserName {
					t.Fatalf("unexpected username: %s", followerSet.Followers[testCase.accountID].UserName)
				}
				if followerSet.Followers[testCase.accountID].DisplayName != testCase.expectedDisplayName {
					t.Fatalf("unexpected display name: %s", followerSet.Followers[testCase.accountID].DisplayName)
				}
				if followerSet.Following[testCase.accountID].UserName != testCase.expectedUserName {
					t.Fatalf("expected following record to be enriched")
				}
			}

			if fetcher.callCount.Load() != testCase.expectedCalls {
				t.Fatalf("unexpected fetcher call count: %d", fetcher.callCount.Load())
			}
		})
	}
}

func copyAccountSets(original matrix.AccountSets) matrix.AccountSets {
	copyFollowers := make(map[string]matrix.AccountRecord, len(original.Followers))
	for accountID, record := range original.Followers {
		copyFollowers[accountID] = record
	}
	copyFollowing := make(map[string]matrix.AccountRecord, len(original.Following))
	for accountID, record := range original.Following {
		copyFollowing[accountID] = record
	}
	copyMuted := make(map[string]bool, len(original.Muted))
	for accountID, muted := range original.Muted {
		copyMuted[accountID] = muted
	}
	copyBlocked := make(map[string]bool, len(original.Blocked))
	for accountID, blocked := range original.Blocked {
		copyBlocked[accountID] = blocked
	}
	return matrix.AccountSets{Followers: copyFollowers, Following: copyFollowing, Muted: copyMuted, Blocked: copyBlocked}
}
