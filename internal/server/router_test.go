package server_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/f-sync/fsync/internal/handles"
	"github.com/f-sync/fsync/internal/matrix"
	"github.com/f-sync/fsync/internal/server"
)

type comparisonServiceStub struct {
	renderedHTML string
	renderError  error
	lastPageData matrix.ComparisonPageData
}

func (stub *comparisonServiceStub) BuildComparison(accountSetsA matrix.AccountSets, accountSetsB matrix.AccountSets, ownerA matrix.OwnerIdentity, ownerB matrix.OwnerIdentity) matrix.ComparisonResult {
	return matrix.ComparisonResult{AccountSetsA: accountSetsA, AccountSetsB: accountSetsB, OwnerA: ownerA, OwnerB: ownerB}
}

func (stub *comparisonServiceStub) RenderComparisonPage(pageData matrix.ComparisonPageData) (string, error) {
	stub.lastPageData = pageData
	return stub.renderedHTML, stub.renderError
}

type matrixComparisonServiceRecorder struct {
	mutex          sync.Mutex
	lastComparison matrix.ComparisonResult
}

func (service *matrixComparisonServiceRecorder) BuildComparison(accountSetsA matrix.AccountSets, accountSetsB matrix.AccountSets, ownerA matrix.OwnerIdentity, ownerB matrix.OwnerIdentity) matrix.ComparisonResult {
	result := matrix.BuildComparison(accountSetsA, accountSetsB, ownerA, ownerB)
	service.mutex.Lock()
	service.lastComparison = result
	service.mutex.Unlock()
	return result
}

func (service *matrixComparisonServiceRecorder) RenderComparisonPage(pageData matrix.ComparisonPageData) (string, error) {
	return "<html>ok</html>", nil
}

func (service *matrixComparisonServiceRecorder) LatestComparison() matrix.ComparisonResult {
	service.mutex.Lock()
	defer service.mutex.Unlock()
	return service.lastComparison
}

type comparisonStoreStub struct {
	snapshot server.ComparisonSnapshot
}

func (stub comparisonStoreStub) Snapshot() server.ComparisonSnapshot {
	return stub.snapshot
}

func (comparisonStoreStub) Upsert(server.ArchiveUpload) (server.ComparisonSnapshot, error) {
	return server.ComparisonSnapshot{}, nil
}

func (comparisonStoreStub) Clear() server.ComparisonSnapshot {
	return server.ComparisonSnapshot{}
}

func (comparisonStoreStub) ApplyResolvedComparison(server.ComparisonData) {}

type handleResolutionRecorder struct {
	results    map[string]handles.Result
	callSignal chan struct{}
}

func newHandleResolutionRecorder(results map[string]handles.Result) *handleResolutionRecorder {
	cloned := make(map[string]handles.Result, len(results))
	for accountID, result := range results {
		cloned[accountID] = result
	}
	return &handleResolutionRecorder{results: cloned, callSignal: make(chan struct{}, 1)}
}

func (resolver *handleResolutionRecorder) ResolveMany(_ context.Context, accountIDs []string) map[string]handles.Result {
	resolved := make(map[string]handles.Result, len(accountIDs))
	for _, accountID := range accountIDs {
		if result, exists := resolver.results[accountID]; exists {
			resolved[accountID] = result
		}
	}
	select {
	case resolver.callSignal <- struct{}{}:
	default:
	}
	return resolved
}

func (resolver *handleResolutionRecorder) waitForCall(timeout time.Duration) bool {
	select {
	case <-resolver.callSignal:
		return true
	case <-time.After(timeout):
		return false
	}
}

type accountHandleResolverStub struct{}

func (accountHandleResolverStub) ResolveMany(context.Context, []string) map[string]handles.Result {
	return nil
}

type staticSnapshotStore struct {
	snapshot server.ComparisonSnapshot
}

func (stub staticSnapshotStore) Snapshot() server.ComparisonSnapshot {
	return stub.snapshot
}

func (staticSnapshotStore) Upsert(server.ArchiveUpload) (server.ComparisonSnapshot, error) {
	return server.ComparisonSnapshot{}, nil
}

func (staticSnapshotStore) Clear() server.ComparisonSnapshot {
	return server.ComparisonSnapshot{}
}

func (staticSnapshotStore) ApplyResolvedComparison(server.ComparisonData) {}

func TestServeComparisonResponses(t *testing.T) {
	const (
		placeholderHTML           = "<html><body>ok</body></html>"
		renderFailureErrorMessage = "render failure"
		expectedRenderError       = "comparison page rendering failed"
	)

	comparisonData := &server.ComparisonData{
		AccountSetsA: matrix.AccountSets{Followers: map[string]matrix.AccountRecord{"1": {AccountID: "1"}}},
		AccountSetsB: matrix.AccountSets{Followers: map[string]matrix.AccountRecord{"2": {AccountID: "2"}}},
		OwnerA:       matrix.OwnerIdentity{AccountID: "1", UserName: "owner_a", DisplayName: "Owner A"},
		OwnerB:       matrix.OwnerIdentity{AccountID: "2", UserName: "owner_b", DisplayName: "Owner B"},
	}

	testCases := []struct {
		name               string
		snapshot           server.ComparisonSnapshot
		service            *comparisonServiceStub
		expectedStatusCode int
		expectedBody       string
		expectComparison   bool
	}{
		{
			name: "renders comparison when data available",
			snapshot: server.ComparisonSnapshot{
				ComparisonData: comparisonData,
				Uploads:        []matrix.UploadSummary{{SlotLabel: "Archive A", OwnerLabel: "Owner A", FileName: "a.zip"}},
			},
			service:            &comparisonServiceStub{renderedHTML: placeholderHTML},
			expectedStatusCode: http.StatusOK,
			expectedBody:       placeholderHTML,
			expectComparison:   true,
		},
		{
			name: "renders empty state when data missing",
			snapshot: server.ComparisonSnapshot{
				Uploads: []matrix.UploadSummary{{SlotLabel: "Archive A", OwnerLabel: "Owner A", FileName: "a.zip"}},
			},
			service:            &comparisonServiceStub{renderedHTML: placeholderHTML},
			expectedStatusCode: http.StatusOK,
			expectedBody:       placeholderHTML,
			expectComparison:   false,
		},
		{
			name:     "render failure returns error",
			snapshot: server.ComparisonSnapshot{ComparisonData: comparisonData},
			service: &comparisonServiceStub{
				renderedHTML: "",
				renderError:  errors.New(renderFailureErrorMessage),
			},
			expectedStatusCode: http.StatusInternalServerError,
			expectedBody:       expectedRenderError,
			expectComparison:   true,
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			router, err := server.NewRouter(server.RouterConfig{
				Service: testCase.service,
				Store:   comparisonStoreStub{snapshot: testCase.snapshot},
			})
			if err != nil {
				t.Fatalf("NewRouter returned error: %v", err)
			}
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			router.ServeHTTP(recorder, request)
			if recorder.Code != testCase.expectedStatusCode {
				t.Fatalf("expected status %d, got %d", testCase.expectedStatusCode, recorder.Code)
			}
			body := recorder.Body.String()
			if !strings.Contains(body, testCase.expectedBody) {
				t.Fatalf("expected body to contain %q, got %q", testCase.expectedBody, body)
			}
			if testCase.service != nil {
				if testCase.expectComparison && testCase.service.lastPageData.Comparison == nil {
					t.Fatalf("expected comparison data to be passed to renderer")
				}
				if !testCase.expectComparison && testCase.service.lastPageData.Comparison != nil {
					t.Fatalf("expected comparison to be nil")
				}
			}
		})
	}
}

func TestUploadArchivesFlow(t *testing.T) {
	const (
		expectedOwnerSummaryArchiveA = "Owner A (@owner_a)"
		expectedOwnerSummaryArchiveB = "Owner B (@owner_b)"
	)
	router, err := server.NewRouter(server.RouterConfig{})
	if err != nil {
		t.Fatalf("NewRouter returned error: %v", err)
	}

	archiveA := createArchive(t, map[string]string{
		"manifest.js":  `{"userInfo":{"accountId":"1","userName":"owner_a","displayName":"Owner A"}}`,
		"following.js": `[{"following":{"accountId":"10","userName":"friend_a","displayName":"Friend A"}}]`,
		"follower.js":  `[{"follower":{"accountId":"11","userName":"follower_a","displayName":"Follower A"}}]`,
	})
	archiveB := createArchive(t, map[string]string{
		"manifest.js":  `{"userInfo":{"accountId":"2","userName":"owner_b","displayName":"Owner B"}}`,
		"following.js": `[{"following":{"accountId":"20","userName":"friend_b","displayName":"Friend B"}}]`,
		"follower.js":  `[{"follower":{"accountId":"21","userName":"follower_b","displayName":"Follower B"}}]`,
	})

	// First upload
	recorder := httptest.NewRecorder()
	request := newUploadRequest(t, archiveA)
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, recorder.Code)
	}
	var firstResponse uploadResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &firstResponse); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if firstResponse.ComparisonReady {
		t.Fatalf("expected comparison to be unavailable after first upload")
	}
	if len(firstResponse.Uploads) != 1 {
		t.Fatalf("expected one upload in response, got %d", len(firstResponse.Uploads))
	}
	if firstResponse.Uploads[0].OwnerLabel != expectedOwnerSummaryArchiveA {
		t.Fatalf("expected owner label %q, got %q", expectedOwnerSummaryArchiveA, firstResponse.Uploads[0].OwnerLabel)
	}

	// Second upload
	recorder = httptest.NewRecorder()
	request = newUploadRequest(t, archiveB)
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, recorder.Code)
	}
	var secondResponse uploadResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &secondResponse); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if !secondResponse.ComparisonReady {
		t.Fatalf("expected comparison to be ready after second upload")
	}
	if len(secondResponse.Uploads) != 2 {
		t.Fatalf("expected two uploads in response, got %d", len(secondResponse.Uploads))
	}
	if secondResponse.Uploads[0].OwnerLabel != expectedOwnerSummaryArchiveA {
		t.Fatalf("expected first owner label %q, got %q", expectedOwnerSummaryArchiveA, secondResponse.Uploads[0].OwnerLabel)
	}
	if secondResponse.Uploads[1].OwnerLabel != expectedOwnerSummaryArchiveB {
		t.Fatalf("expected second owner label %q, got %q", expectedOwnerSummaryArchiveB, secondResponse.Uploads[1].OwnerLabel)
	}

	// Ensure GET renders HTML containing the owner name
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/", nil)
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "Owner A (@owner_a) — Relationship Matrix") {
		t.Fatalf("expected rendered page to include owner name")
	}
}

func TestStartComparisonTaskValidations(t *testing.T) {
	comparisonReadySnapshot := server.ComparisonSnapshot{
		ComparisonData: &server.ComparisonData{
			AccountSetsA: matrix.AccountSets{Followers: map[string]matrix.AccountRecord{"100": {AccountID: "100"}}},
			AccountSetsB: matrix.AccountSets{Followers: map[string]matrix.AccountRecord{"200": {AccountID: "200"}}},
			OwnerA:       matrix.OwnerIdentity{AccountID: "1"},
			OwnerB:       matrix.OwnerIdentity{AccountID: "2"},
			FileNameA:    "primary.zip",
			FileNameB:    "secondary.zip",
		},
	}

	testCases := []struct {
		name           string
		resolver       matrix.AccountHandleResolver
		snapshot       server.ComparisonSnapshot
		expectedStatus int
		expectedError  string
	}{
		{
			name:           "missing resolver returns error",
			resolver:       nil,
			snapshot:       comparisonReadySnapshot,
			expectedStatus: http.StatusBadRequest,
			expectedError:  "handle resolver is not configured",
		},
		{
			name:           "missing comparison returns error",
			resolver:       accountHandleResolverStub{},
			snapshot:       server.ComparisonSnapshot{},
			expectedStatus: http.StatusBadRequest,
			expectedError:  "comparison is not ready; upload two archives first",
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			router, err := server.NewRouter(server.RouterConfig{
				Store:          staticSnapshotStore{snapshot: testCase.snapshot},
				HandleResolver: testCase.resolver,
			})
			if err != nil {
				t.Fatalf("NewRouter returned error: %v", err)
			}

			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/api/compare", nil)
			router.ServeHTTP(recorder, request)
			if recorder.Code != testCase.expectedStatus {
				t.Fatalf("expected status %d, got %d", testCase.expectedStatus, recorder.Code)
			}

			var response uploadErrorResponse
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatalf("decode error response: %v", err)
			}
			if !strings.Contains(response.Error, testCase.expectedError) {
				t.Fatalf("expected error message to contain %q, got %q", testCase.expectedError, response.Error)
			}
		})
	}
}

func TestComparisonTaskProgress(t *testing.T) {
	const (
		accountIDFirst  = "100"
		accountIDSecond = "200"
		expectedTotal   = 2
	)

	comparisonData := &server.ComparisonData{
		AccountSetsA: matrix.AccountSets{Followers: map[string]matrix.AccountRecord{accountIDFirst: {AccountID: accountIDFirst}}},
		AccountSetsB: matrix.AccountSets{Followers: map[string]matrix.AccountRecord{accountIDSecond: {AccountID: accountIDSecond}}},
		OwnerA:       matrix.OwnerIdentity{AccountID: "1"},
		OwnerB:       matrix.OwnerIdentity{AccountID: "2"},
		FileNameA:    "a.zip",
		FileNameB:    "b.zip",
	}

	store := staticSnapshotStore{snapshot: server.ComparisonSnapshot{ComparisonData: comparisonData}}
	resolver := newHandleResolutionRecorder(map[string]handles.Result{
		accountIDFirst:  {Record: handles.AccountRecord{AccountID: accountIDFirst, UserName: "first_user"}},
		accountIDSecond: {Record: handles.AccountRecord{AccountID: accountIDSecond, UserName: "second_user"}},
	})

	router, err := server.NewRouter(server.RouterConfig{Store: store, HandleResolver: resolver})
	if err != nil {
		t.Fatalf("NewRouter returned error: %v", err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/compare", nil)
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d", http.StatusAccepted, recorder.Code)
	}

	var taskResponse compareTaskResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &taskResponse); err != nil {
		t.Fatalf("decode task response: %v", err)
	}
	if taskResponse.TaskID == "" {
		t.Fatalf("expected task identifier to be set")
	}
	if taskResponse.Total != expectedTotal {
		t.Fatalf("expected total %d, got %d", expectedTotal, taskResponse.Total)
	}

	progress := waitForComparisonTask(t, router, taskResponse.TaskID)
	if progress.Status != "completed" {
		t.Fatalf("expected task status completed, got %s", progress.Status)
	}
	if progress.Completed != expectedTotal {
		t.Fatalf("expected completed count %d, got %d", expectedTotal, progress.Completed)
	}
	if len(progress.Errors) != 0 {
		t.Fatalf("expected no resolution errors, got %v", progress.Errors)
	}
}

func TestComparisonUsesResolvedHandlesForBlockedOnlyAccounts(t *testing.T) {
	const (
		ownerAccountIDA      = "1"
		ownerAccountIDB      = "2"
		followingAccountIDA  = "10"
		followerAccountIDA   = "11"
		blockedAccountIDA    = "100"
		blockedUserNameA     = "blocked_handle_a"
		blockedDisplayNameA  = "Blocked Name A"
		followingAccountIDB  = "20"
		followerAccountIDB   = "21"
		blockedAccountIDB    = "200"
		blockedUserNameB     = "blocked_handle_b"
		blockedDisplayNameB  = "Blocked Name B"
		expectedStatusCodeOK = http.StatusOK
	)

	service := &matrixComparisonServiceRecorder{}
	handleResults := map[string]handles.Result{
		followingAccountIDA: {Record: handles.AccountRecord{AccountID: followingAccountIDA, UserName: "friend_a", DisplayName: "Friend A"}},
		followerAccountIDA:  {Record: handles.AccountRecord{AccountID: followerAccountIDA, UserName: "follower_a", DisplayName: "Follower A"}},
		blockedAccountIDA:   {Record: handles.AccountRecord{AccountID: blockedAccountIDA, UserName: blockedUserNameA, DisplayName: blockedDisplayNameA}},
		followingAccountIDB: {Record: handles.AccountRecord{AccountID: followingAccountIDB, UserName: "friend_b", DisplayName: "Friend B"}},
		followerAccountIDB:  {Record: handles.AccountRecord{AccountID: followerAccountIDB, UserName: "follower_b", DisplayName: "Follower B"}},
		blockedAccountIDB:   {Record: handles.AccountRecord{AccountID: blockedAccountIDB, UserName: blockedUserNameB, DisplayName: blockedDisplayNameB}},
	}
	resolver := newHandleResolutionRecorder(handleResults)

	router, err := server.NewRouter(server.RouterConfig{
		Service:        service,
		HandleResolver: resolver,
	})
	if err != nil {
		t.Fatalf("NewRouter returned error: %v", err)
	}

	archiveA := createArchive(t, map[string]string{
		"manifest.js":  `{"userInfo":{"accountId":"` + ownerAccountIDA + `","userName":"owner_a","displayName":"Owner A"}}`,
		"following.js": `[{"following":{"accountId":"` + followingAccountIDA + `","userName":"friend_a","displayName":"Friend A"}}]`,
		"follower.js":  `[{"follower":{"accountId":"` + followerAccountIDA + `","userName":"follower_a","displayName":"Follower A"}}]`,
		"block.js":     `[{"blocking":{"accountId":"` + blockedAccountIDA + `"}}]`,
	})
	archiveB := createArchive(t, map[string]string{
		"manifest.js":  `{"userInfo":{"accountId":"` + ownerAccountIDB + `","userName":"owner_b","displayName":"Owner B"}}`,
		"following.js": `[{"following":{"accountId":"` + followingAccountIDB + `","userName":"friend_b","displayName":"Friend B"}}]`,
		"follower.js":  `[{"follower":{"accountId":"` + followerAccountIDB + `","userName":"follower_b","displayName":"Follower B"}}]`,
		"block.js":     `[{"blocking":{"accountId":"` + blockedAccountIDB + `"}}]`,
	})

	recorder := httptest.NewRecorder()
	request := newUploadRequest(t, archiveA)
	router.ServeHTTP(recorder, request)
	if recorder.Code != expectedStatusCodeOK {
		t.Fatalf("expected status %d, got %d", expectedStatusCodeOK, recorder.Code)
	}

	recorder = httptest.NewRecorder()
	request = newUploadRequest(t, archiveB)
	router.ServeHTTP(recorder, request)
	if recorder.Code != expectedStatusCodeOK {
		t.Fatalf("expected status %d, got %d", expectedStatusCodeOK, recorder.Code)
	}

	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/api/compare", nil)
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d", http.StatusAccepted, recorder.Code)
	}

	var taskResponse compareTaskResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &taskResponse); err != nil {
		t.Fatalf("decode task response: %v", err)
	}
	progress := waitForComparisonTask(t, router, taskResponse.TaskID)
	if progress.Status != "completed" {
		t.Fatalf("expected task status completed, got %s", progress.Status)
	}

	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/", nil)
	router.ServeHTTP(recorder, request)
	if recorder.Code != expectedStatusCodeOK {
		t.Fatalf("expected status %d, got %d", expectedStatusCodeOK, recorder.Code)
	}

	comparison := service.LatestComparison()

	blockedRecordA, foundA := findAccountRecordByID(comparison.OwnerABlockedAll, blockedAccountIDA)
	blockedRecordB, foundB := findAccountRecordByID(comparison.OwnerBBlockedAll, blockedAccountIDB)
	if !foundA || !foundB {
		t.Fatalf("expected blocked records for both owners")
	}
	if blockedRecordA.DisplayName != blockedDisplayNameA || blockedRecordA.UserName != blockedUserNameA {
		t.Fatalf("unexpected owner A blocked metadata: %+v", blockedRecordA)
	}
	if blockedRecordB.DisplayName != blockedDisplayNameB || blockedRecordB.UserName != blockedUserNameB {
		t.Fatalf("unexpected owner B blocked metadata: %+v", blockedRecordB)
	}

	testCases := []struct {
		name                string
		accountID           string
		expectedDisplayName string
		expectedUserName    string
		recordsSelector     func(matrix.ComparisonResult) []matrix.AccountRecord
	}{
		{
			name:                "owner a blocked metadata uses resolved handle",
			accountID:           blockedAccountIDA,
			expectedDisplayName: blockedDisplayNameA,
			expectedUserName:    blockedUserNameA,
			recordsSelector: func(result matrix.ComparisonResult) []matrix.AccountRecord {
				return result.OwnerABlockedAll
			},
		},
		{
			name:                "owner b blocked metadata uses resolved handle",
			accountID:           blockedAccountIDB,
			expectedDisplayName: blockedDisplayNameB,
			expectedUserName:    blockedUserNameB,
			recordsSelector: func(result matrix.ComparisonResult) []matrix.AccountRecord {
				return result.OwnerBBlockedAll
			},
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			record, found := findAccountRecordByID(testCase.recordsSelector(comparison), testCase.accountID)
			if !found {
				t.Fatalf("expected blocked record for %s", testCase.accountID)
			}
			if record.DisplayName != testCase.expectedDisplayName {
				t.Fatalf("unexpected display name: %s", record.DisplayName)
			}
			if record.UserName != testCase.expectedUserName {
				t.Fatalf("unexpected username: %s", record.UserName)
			}
		})
	}
}

func TestUploadArchivesRejectsInvalidZip(t *testing.T) {
	router, err := server.NewRouter(server.RouterConfig{})
	if err != nil {
		t.Fatalf("NewRouter returned error: %v", err)
	}

	invalidArchive := createArchive(t, map[string]string{
		"manifest.js": `{"userInfo":{"accountId":"3"}}`,
	})

	recorder := httptest.NewRecorder()
	request := newUploadRequest(t, invalidArchive)
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, recorder.Code)
	}
	var response uploadErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if !strings.Contains(response.Error, "uploaded file must be a Twitter archive zip") {
		t.Fatalf("expected error message to mention invalid archive, got %q", response.Error)
	}
}

func TestStaticAssetServed(t *testing.T) {
	router, err := server.NewRouter(server.RouterConfig{})
	if err != nil {
		t.Fatalf("NewRouter returned error: %v", err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/static/app.js", nil)
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "initializeUploadUI") {
		t.Fatalf("expected JS bundle to include upload initializer")
	}
}

type uploadResponse struct {
	Uploads         []matrix.UploadSummary `json:"uploads"`
	ComparisonReady bool                   `json:"comparisonReady"`
}

type uploadErrorResponse struct {
	Error string `json:"error"`
}

type compareTaskResponse struct {
	TaskID string `json:"taskID"`
	Total  int    `json:"total"`
}

type compareProgressResponse struct {
	TaskID    string            `json:"taskID"`
	Total     int               `json:"total"`
	Completed int               `json:"completed"`
	Status    string            `json:"status"`
	Errors    map[string]string `json:"errors"`
}

func waitForComparisonTask(t *testing.T, handler http.Handler, taskID string) compareProgressResponse {
	t.Helper()
	const (
		maxAttempts         = 50
		pollInterval        = 20 * time.Millisecond
		terminalStatus      = "completed"
		terminalErrorStatus = "failed"
	)

	for attempt := 0; attempt < maxAttempts; attempt++ {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/compare/%s", taskID), nil)
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("unexpected progress status %d: %s", recorder.Code, recorder.Body.String())
		}

		var progress compareProgressResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &progress); err != nil {
			t.Fatalf("decode progress response: %v", err)
		}
		if progress.Status == terminalStatus || progress.Status == terminalErrorStatus {
			return progress
		}
		time.Sleep(pollInterval)
	}
	t.Fatalf("comparison task %s did not complete", taskID)
	return compareProgressResponse{}
}

func findAccountRecordByID(records []matrix.AccountRecord, accountID string) (matrix.AccountRecord, bool) {
	for _, record := range records {
		if record.AccountID == accountID {
			return record, true
		}
	}
	return matrix.AccountRecord{}, false
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
		t.Fatalf("close writer: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/uploads", body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
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
			t.Fatalf("create entry: %v", err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatalf("write entry: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close archive writer: %v", err)
	}
	return archivePath
}
