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
	stubResolverUserName              = "stub-resolved"
	stubResolverDisplayName           = "Stub Resolved"
	archivedUserName                  = "archived"
	archivedDisplayName               = "Archived Name"
	resolvedUserName                  = "resolved"
	resolvedDisplayName               = "Resolved Name"
	stubFetchErrorMessage             = "fetch failed"
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

func (resolver *stubResolver) ResolveMany(_ context.Context, accountIDs []string) map[string]handles.Result {
	resolver.callCount.Add(1)
	results := make(map[string]handles.Result, len(accountIDs))
	for _, accountID := range accountIDs {
		results[accountID] = handles.Result{
			Record: handles.AccountRecord{
				AccountID:   accountID,
				UserName:    stubResolverUserName,
				DisplayName: stubResolverDisplayName,
			},
		}
	}
	return results
}

func TestResolveHandlesResolverAvailability(t *testing.T) {
	decoratedRecord := matrix.AccountRecord{AccountID: matrixTestAccountIDDisabled, UserName: archivedUserName, DisplayName: archivedDisplayName}
	baseAccountSets := matrix.AccountSets{Followers: map[string]matrix.AccountRecord{matrixTestAccountIDDisabled: decoratedRecord}, Following: map[string]matrix.AccountRecord{matrixTestAccountIDDisabled: decoratedRecord}}

	testCases := []struct {
		name                string
		resolver            matrix.AccountHandleResolver
		expectedCalls       int32
		expectedUserName    string
		expectedDisplayName string
	}{
		{
			name:                "resolver provided",
			resolver:            &stubResolver{},
			expectedCalls:       1,
			expectedUserName:    stubResolverUserName,
			expectedDisplayName: stubResolverDisplayName,
		},
		{
			name:                "resolver missing",
			resolver:            nil,
			expectedCalls:       0,
			expectedUserName:    archivedUserName,
			expectedDisplayName: archivedDisplayName,
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

			result := matrix.ResolveHandles(context.Background(), testCase.resolver, &followerSet)
			if result != nil {
				t.Fatalf("expected nil result, got %v", result)
			}
			if callCounter == nil {
				if testCase.expectedCalls != 0 {
					t.Fatalf("expected resolver to be invoked %d times", testCase.expectedCalls)
				}
			} else if callCounter.Load() != testCase.expectedCalls {
				t.Fatalf("unexpected resolver call count: %d", callCounter.Load())
			}

			followerRecord := followerSet.Followers[matrixTestAccountIDDisabled]
			if followerRecord.UserName != testCase.expectedUserName {
				t.Fatalf("unexpected follower username: %s", followerRecord.UserName)
			}
			if followerRecord.DisplayName != testCase.expectedDisplayName {
				t.Fatalf("unexpected follower display name: %s", followerRecord.DisplayName)
			}

			followingRecord := followerSet.Following[matrixTestAccountIDDisabled]
			if followingRecord.UserName != testCase.expectedUserName {
				t.Fatalf("unexpected following username: %s", followingRecord.UserName)
			}
			if followingRecord.DisplayName != testCase.expectedDisplayName {
				t.Fatalf("unexpected following display name: %s", followingRecord.DisplayName)
			}
		})
	}
}

func TestResolveHandlesResolution(t *testing.T) {
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
			expectedUserName:    resolvedUserName,
			expectedDisplayName: resolvedDisplayName,
			expectedCalls:       1,
		},
		{
			name:                "missing handle in html",
			accountID:           matrixTestAccountIDMissingHandle,
			htmlContent:         stubIntentHTMLMissingHandle,
			expectError:         true,
			expectedUserName:    archivedUserName,
			expectedDisplayName: archivedDisplayName,
			expectedCalls:       1,
		},
		{
			name:                "fetcher error",
			accountID:           matrixTestAccountIDFetcherFailure,
			fetchError:          errors.New(stubFetchErrorMessage),
			expectError:         true,
			expectedUserName:    archivedUserName,
			expectedDisplayName: archivedDisplayName,
			expectedCalls:       1,
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

			decoratedRecord := matrix.AccountRecord{AccountID: testCase.accountID, UserName: archivedUserName, DisplayName: archivedDisplayName}
			baseAccountSets := matrix.AccountSets{
				Followers: map[string]matrix.AccountRecord{testCase.accountID: decoratedRecord},
				Following: map[string]matrix.AccountRecord{testCase.accountID: decoratedRecord},
			}

			followerSet := copyAccountSets(baseAccountSets)
			result := matrix.ResolveHandles(context.Background(), resolver, &followerSet)

			if testCase.expectError {
				if len(result) == 0 {
					t.Fatalf("expected errors from resolution")
				}
				if _, exists := result[testCase.accountID]; !exists {
					t.Fatalf("expected error entry for %s", testCase.accountID)
				}
			} else {
				if result != nil {
					t.Fatalf("expected no errors, received %v", result)
				}
			}

			followerRecord := followerSet.Followers[testCase.accountID]
			if followerRecord.UserName != testCase.expectedUserName {
				t.Fatalf("unexpected username: %s", followerRecord.UserName)
			}
			if followerRecord.DisplayName != testCase.expectedDisplayName {
				t.Fatalf("unexpected display name: %s", followerRecord.DisplayName)
			}

			followingRecord := followerSet.Following[testCase.accountID]
			if followingRecord.UserName != testCase.expectedUserName {
				t.Fatalf("unexpected following username: %s", followingRecord.UserName)
			}
			if followingRecord.DisplayName != testCase.expectedDisplayName {
				t.Fatalf("unexpected following display name: %s", followingRecord.DisplayName)
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
