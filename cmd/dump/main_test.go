package main_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	dumpcmd "github.com/f-sync/fsync/cmd/dump"
	"github.com/f-sync/fsync/internal/handles"
)

type stubResolver struct {
	callCount atomic.Int32
}

func (resolver *stubResolver) ResolveMany(_ context.Context, _ []string) map[string]handles.Result {
	resolver.callCount.Add(1)
	return nil
}

func TestMaybeResolveHandlesDisabled(t *testing.T) {
	followerSet := dumpcmd.AccountSets{Followers: map[string]dumpcmd.AccountRecord{"1": {AccountID: "1"}}, Following: map[string]dumpcmd.AccountRecord{"1": {AccountID: "1"}}}
	resolver := &stubResolver{}

	result := dumpcmd.MaybeResolveHandles(context.Background(), resolver, false, &followerSet)
	if result != nil {
		t.Fatalf("expected nil result, got %v", result)
	}
	if resolver.callCount.Load() != 0 {
		t.Fatalf("expected resolver to remain unused")
	}
	if followerSet.Followers["1"].UserName != "" {
		t.Fatalf("expected follower username to remain empty")
	}
}

func TestMaybeResolveHandlesEnabled(t *testing.T) {
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

			followerSet := dumpcmd.AccountSets{Followers: map[string]dumpcmd.AccountRecord{"1": {AccountID: "1"}}, Following: map[string]dumpcmd.AccountRecord{"1": {AccountID: "1"}}}
			result := dumpcmd.MaybeResolveHandles(context.Background(), resolver, true, &followerSet)

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
