package tests

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/f-sync/fsync/internal/handles"
	"github.com/f-sync/fsync/internal/server"
)

const (
	integrationOwnerAccountIDA     = "1"
	integrationOwnerAccountIDB     = "2"
	integrationFollowingAccountIDA = "10"
	integrationFollowerAccountIDA  = "11"
	integrationBlockedAccountIDA   = "100"
	integrationFollowingAccountIDB = "20"
	integrationFollowerAccountIDB  = "21"
	integrationBlockedAccountIDB   = "200"
	integrationResolvedHandleA     = "blocked_handle_a"
	integrationResolvedNameA       = "Blocked Name A"
	integrationResolvedHandleB     = "blocked_handle_b"
	integrationResolvedNameB       = "Blocked Name B"
	integrationProgressTimeout     = 10 * time.Second
	integrationProgressPollDelay   = 200 * time.Millisecond
)

type comparisonTaskResponse struct {
	TaskID string `json:"taskID"`
	Total  int    `json:"total"`
}

type comparisonProgressResponse struct {
	Status    string            `json:"status"`
	Completed int               `json:"completed"`
	Errors    map[string]string `json:"errors"`
}

type handleResolverStub struct {
	results map[string]handles.Result
}

func (stub handleResolverStub) ResolveMany(_ context.Context, accountIDs []string) map[string]handles.Result {
	resolved := make(map[string]handles.Result, len(accountIDs))
	for _, accountID := range accountIDs {
		if result, exists := stub.results[accountID]; exists {
			resolved[accountID] = result
		}
	}
	return resolved
}

func TestComparisonProgressLifecycle(t *testing.T) {
	testCases := []struct {
		name string
	}{
		{name: "resolves handles and completes"},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			resolver := handleResolverStub{results: map[string]handles.Result{
				integrationFollowingAccountIDA: {Record: handles.AccountRecord{AccountID: integrationFollowingAccountIDA, UserName: "friend_a"}},
				integrationFollowerAccountIDA:  {Record: handles.AccountRecord{AccountID: integrationFollowerAccountIDA, UserName: "follower_a"}},
				integrationBlockedAccountIDA:   {Record: handles.AccountRecord{AccountID: integrationBlockedAccountIDA, UserName: integrationResolvedHandleA, DisplayName: integrationResolvedNameA}},
				integrationFollowingAccountIDB: {Record: handles.AccountRecord{AccountID: integrationFollowingAccountIDB, UserName: "friend_b"}},
				integrationFollowerAccountIDB:  {Record: handles.AccountRecord{AccountID: integrationFollowerAccountIDB, UserName: "follower_b"}},
				integrationBlockedAccountIDB:   {Record: handles.AccountRecord{AccountID: integrationBlockedAccountIDB, UserName: integrationResolvedHandleB, DisplayName: integrationResolvedNameB}},
			}}

			router, err := server.NewRouter(server.RouterConfig{HandleResolver: resolver})
			if err != nil {
				t.Fatalf("NewRouter returned error: %v", err)
			}

			archiveA := createArchive(t, map[string]string{
				"manifest.js":  fmt.Sprintf(`{"userInfo":{"accountId":"%s","userName":"owner_a","displayName":"Owner A"}}`, integrationOwnerAccountIDA),
				"following.js": fmt.Sprintf(`[{"following":{"accountId":"%s"}}]`, integrationFollowingAccountIDA),
				"follower.js":  fmt.Sprintf(`[{"follower":{"accountId":"%s"}}]`, integrationFollowerAccountIDA),
				"block.js":     fmt.Sprintf(`[{"blocking":{"accountId":"%s"}}]`, integrationBlockedAccountIDA),
			})
			archiveB := createArchive(t, map[string]string{
				"manifest.js":  fmt.Sprintf(`{"userInfo":{"accountId":"%s","userName":"owner_b","displayName":"Owner B"}}`, integrationOwnerAccountIDB),
				"following.js": fmt.Sprintf(`[{"following":{"accountId":"%s"}}]`, integrationFollowingAccountIDB),
				"follower.js":  fmt.Sprintf(`[{"follower":{"accountId":"%s"}}]`, integrationFollowerAccountIDB),
				"block.js":     fmt.Sprintf(`[{"blocking":{"accountId":"%s"}}]`, integrationBlockedAccountIDB),
			})

			recorder := httptest.NewRecorder()
			request := newUploadRequest(t, archiveA)
			router.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusOK {
				t.Fatalf("expected status %d, got %d", http.StatusOK, recorder.Code)
			}

			recorder = httptest.NewRecorder()
			request = newUploadRequest(t, archiveB)
			router.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusOK {
				t.Fatalf("expected status %d, got %d", http.StatusOK, recorder.Code)
			}

			recorder = httptest.NewRecorder()
			request = httptest.NewRequest(http.MethodPost, "/api/compare", nil)
			router.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusAccepted {
				t.Fatalf("expected status %d, got %d", http.StatusAccepted, recorder.Code)
			}
			var task comparisonTaskResponse
			if err := json.Unmarshal(recorder.Body.Bytes(), &task); err != nil {
				t.Fatalf("decode task response: %v", err)
			}
			if task.TaskID == "" {
				t.Fatalf("task identifier missing in response")
			}

			waitForTaskCompletion(t, router, task.TaskID)

			recorder = httptest.NewRecorder()
			request = httptest.NewRequest(http.MethodGet, "/", nil)
			router.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusOK {
				t.Fatalf("expected status %d, got %d", http.StatusOK, recorder.Code)
			}
			body := recorder.Body.String()
			if !containsAll(body, integrationResolvedHandleA, integrationResolvedHandleB) {
				t.Fatalf("expected comparison HTML to contain resolved handles, got %s", body)
			}
		})
	}
}

func waitForTaskCompletion(t *testing.T, handler http.Handler, taskID string) {
	t.Helper()
	deadline := time.Now().Add(integrationProgressTimeout)
	for time.Now().Before(deadline) {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/compare/%s", taskID), nil)
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("unexpected progress status %d: %s", recorder.Code, recorder.Body.String())
		}
		var progress comparisonProgressResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &progress); err != nil {
			t.Fatalf("decode progress response: %v", err)
		}
		if progress.Status == "completed" {
			if len(progress.Errors) != 0 {
				t.Fatalf("expected no resolution errors, got %v", progress.Errors)
			}
			return
		}
		time.Sleep(integrationProgressPollDelay)
	}
	t.Fatalf("comparison task %s did not complete", taskID)
}

func createArchive(t *testing.T, files map[string]string) string {
	t.Helper()
	tempDir := t.TempDir()
	archivePath := filepath.Join(tempDir, "archive.zip")

	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	defer file.Close()

	writer := zip.NewWriter(file)
	for name, content := range files {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatalf("create archive entry: %v", err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatalf("write archive entry: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close archive writer: %v", err)
	}
	return archivePath
}

func newUploadRequest(t *testing.T, archivePath string) *http.Request {
	t.Helper()
	file, err := os.Open(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer file.Close()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("archives", filepath.Base(archivePath))
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := io.Copy(part, file); err != nil {
		t.Fatalf("copy archive: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/uploads", body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
}

func containsAll(haystack string, values ...string) bool {
	for _, value := range values {
		if !strings.Contains(haystack, value) {
			return false
		}
	}
	return true
}
