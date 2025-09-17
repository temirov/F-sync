package matrix_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/f-sync/fsync/internal/handles"
	"github.com/f-sync/fsync/internal/matrix"
)

type stubResolver struct {
	callCount atomic.Int32
}

func (resolver *stubResolver) ResolveMany(_ context.Context, _ []string) map[string]handles.Result {
	resolver.callCount.Add(1)
	return nil
}

func TestMaybeResolveHandlesDisabled(t *testing.T) {
	decoratedRecord := matrix.AccountRecord{AccountID: "1"}
	baseAccountSets := matrix.AccountSets{Followers: map[string]matrix.AccountRecord{"1": decoratedRecord}, Following: map[string]matrix.AccountRecord{"1": decoratedRecord}}

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
			if followerSet.Followers["1"].UserName != "" {
				t.Fatalf("expected follower username to remain empty")
			}
		})
	}
}

func TestMaybeResolveHandlesNetwork(t *testing.T) {
	decoratedRecord := matrix.AccountRecord{AccountID: "1"}
	baseAccountSets := matrix.AccountSets{Followers: map[string]matrix.AccountRecord{"1": decoratedRecord}, Following: map[string]matrix.AccountRecord{"1": decoratedRecord}}

	testCases := []struct {
		name             string
		configureServer  func(headCount *atomic.Int32, profileCount *atomic.Int32) *httptest.Server
		expectError      bool
		expectedUserName string
		expectedName     string
	}{
		{
			name: "successful resolution",
			configureServer: func(headCount *atomic.Int32, profileCount *atomic.Int32) *httptest.Server {
				var server *httptest.Server
				profilePath := "/resolved"
				server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					switch {
					case request.URL.Path == profilePath:
						profileCount.Add(1)
						writer.WriteHeader(http.StatusOK)
						_, _ = writer.Write([]byte("<title>Resolved Name (@resolved) / X</title>"))
						return
					case request.URL.Path == "/i/user/1":
						headCount.Add(1)
						writer.Header().Set("Location", server.URL+profilePath)
						writer.WriteHeader(http.StatusFound)
						return
					default:
						writer.WriteHeader(http.StatusNotFound)
					}
				}))
				return server
			},
			expectedUserName: "resolved",
			expectedName:     "Resolved Name",
		},
		{
			name: "resolution failure",
			configureServer: func(headCount *atomic.Int32, profileCount *atomic.Int32) *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					if request.URL.Path == "/i/user/1" {
						headCount.Add(1)
						writer.WriteHeader(http.StatusInternalServerError)
						return
					}
					writer.WriteHeader(http.StatusNotFound)
				}))
			},
			expectError: true,
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			var headCount atomic.Int32
			var profileCount atomic.Int32
			server := testCase.configureServer(&headCount, &profileCount)
			t.Cleanup(server.Close)

			resolver, err := handles.NewResolver(handles.Config{BaseURL: server.URL, Client: server.Client(), MaxConcurrent: 2})
			if err != nil {
				t.Fatalf("create resolver: %v", err)
			}

			followerSet := copyAccountSets(baseAccountSets)
			result := matrix.MaybeResolveHandles(context.Background(), resolver, true, &followerSet)

			if testCase.expectError {
				if len(result) == 0 {
					t.Fatalf("expected errors from resolution")
				}
				if followerSet.Followers["1"].UserName != "" {
					t.Fatalf("expected username to remain empty after failure")
				}
				return
			}

			if len(result) != 0 {
				t.Fatalf("expected no errors, received %v", result)
			}
			if followerSet.Followers["1"].UserName != testCase.expectedUserName {
				t.Fatalf("unexpected username: %s", followerSet.Followers["1"].UserName)
			}
			if followerSet.Followers["1"].DisplayName != testCase.expectedName {
				t.Fatalf("unexpected display name: %s", followerSet.Followers["1"].DisplayName)
			}
			if followerSet.Following["1"].UserName != testCase.expectedUserName {
				t.Fatalf("expected following record to be enriched")
			}
			if headCount.Load() != 1 {
				t.Fatalf("expected a single redirect lookup, got %d", headCount.Load())
			}
			if profileCount.Load() != 1 {
				t.Fatalf("expected a single profile fetch, got %d", profileCount.Load())
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
