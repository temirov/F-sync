package server_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

const (
	stubSlotLabelPrimary          = "Archive A"
	stubSlotLabelSecondary        = "Archive B"
	handleResolutionWaitDuration  = 500 * time.Millisecond
	handleResolutionNoCallTimeout = 100 * time.Millisecond
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

func (comparisonStoreStub) ResolveHandles(context.Context, matrix.AccountHandleResolver) map[string]error {
	return nil
}

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

type resolvingStoreStub struct {
	uploads        []server.ArchiveUpload
	resolveSignals chan struct{}
	mutex          sync.Mutex
}

func newResolvingStoreStub() *resolvingStoreStub {
	return &resolvingStoreStub{resolveSignals: make(chan struct{}, 1)}
}

func (stub *resolvingStoreStub) Snapshot() server.ComparisonSnapshot {
	stub.mutex.Lock()
	defer stub.mutex.Unlock()
	return stub.snapshotLocked()
}

func (stub *resolvingStoreStub) Upsert(upload server.ArchiveUpload) (server.ComparisonSnapshot, error) {
	stub.mutex.Lock()
	defer stub.mutex.Unlock()
	stub.uploads = append(stub.uploads, upload)
	return stub.snapshotLocked(), nil
}

func (stub *resolvingStoreStub) Clear() server.ComparisonSnapshot {
	stub.mutex.Lock()
	defer stub.mutex.Unlock()
	stub.uploads = nil
	return server.ComparisonSnapshot{}
}

func (stub *resolvingStoreStub) ResolveHandles(context.Context, matrix.AccountHandleResolver) map[string]error {
	select {
	case stub.resolveSignals <- struct{}{}:
	default:
	}
	return nil
}

func (stub *resolvingStoreStub) snapshotLocked() server.ComparisonSnapshot {
	uploads := make([]matrix.UploadSummary, 0, len(stub.uploads))
	for index, upload := range stub.uploads {
		slotLabel := stubSlotLabelPrimary
		if index == 1 {
			slotLabel = stubSlotLabelSecondary
		}
		uploads = append(uploads, matrix.UploadSummary{
			SlotLabel:  slotLabel,
			OwnerLabel: upload.Owner.DisplayName,
			FileName:   upload.FileName,
		})
	}
	var comparisonData *server.ComparisonData
	if len(stub.uploads) >= 2 {
		comparisonData = &server.ComparisonData{
			AccountSetsA: stub.uploads[0].AccountSets,
			AccountSetsB: stub.uploads[1].AccountSets,
			OwnerA:       stub.uploads[0].Owner,
			OwnerB:       stub.uploads[1].Owner,
		}
	}
	return server.ComparisonSnapshot{Uploads: uploads, ComparisonData: comparisonData}
}

func (stub *resolvingStoreStub) waitForResolveHandles(timeout time.Duration) bool {
	select {
	case <-stub.resolveSignals:
		return true
	case <-time.After(timeout):
		return false
	}
}

type accountHandleResolverStub struct{}

func (accountHandleResolverStub) ResolveMany(context.Context, []string) map[string]handles.Result {
	return nil
}

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

func TestUploadArchivesHandleResolution(t *testing.T) {
	primaryArchiveContents := map[string]string{
		"manifest.js":  `{"userInfo":{"accountId":"1","userName":"owner_a","displayName":"Owner A"}}`,
		"following.js": `[{"following":{"accountId":"10","userName":"friend_a","displayName":"Friend A"}}]`,
		"follower.js":  `[{"follower":{"accountId":"11","userName":"follower_a","displayName":"Follower A"}}]`,
	}
	secondaryArchiveContents := map[string]string{
		"manifest.js":  `{"userInfo":{"accountId":"2","userName":"owner_b","displayName":"Owner B"}}`,
		"following.js": `[{"following":{"accountId":"20","userName":"friend_b","displayName":"Friend B"}}]`,
		"follower.js":  `[{"follower":{"accountId":"21","userName":"follower_b","displayName":"Follower B"}}]`,
	}

	testCases := []struct {
		name             string
		handleResolver   matrix.AccountHandleResolver
		expectResolution bool
	}{
		{
			name:             "handle resolver triggers asynchronous resolution",
			handleResolver:   accountHandleResolverStub{},
			expectResolution: true,
		},
		{
			name:             "missing resolver skips asynchronous resolution",
			handleResolver:   nil,
			expectResolution: false,
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			store := newResolvingStoreStub()
			router, err := server.NewRouter(server.RouterConfig{
				Store:          store,
				HandleResolver: testCase.handleResolver,
			})
			if err != nil {
				t.Fatalf("NewRouter returned error: %v", err)
			}

			archiveA := createArchive(t, primaryArchiveContents)
			archiveB := createArchive(t, secondaryArchiveContents)

			recorder := httptest.NewRecorder()
			request := newUploadRequest(t, archiveA)
			router.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusOK {
				t.Fatalf("expected status %d, got %d", http.StatusOK, recorder.Code)
			}
			if store.waitForResolveHandles(handleResolutionNoCallTimeout) {
				t.Fatalf("handle resolution triggered before comparison was ready")
			}

			recorder = httptest.NewRecorder()
			request = newUploadRequest(t, archiveB)
			router.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusOK {
				t.Fatalf("expected status %d, got %d", http.StatusOK, recorder.Code)
			}

			if testCase.expectResolution {
				if !store.waitForResolveHandles(handleResolutionWaitDuration) {
					t.Fatalf("expected handle resolution to be triggered")
				}
			} else {
				if store.waitForResolveHandles(handleResolutionNoCallTimeout) {
					t.Fatalf("did not expect handle resolution to be triggered")
				}
			}
		})
	}
}

func TestComparisonUsesResolvedHandlesForBlockedOnlyAccounts(t *testing.T) {
	const (
		ownerAccountIDA             = "1"
		ownerAccountIDB             = "2"
		followingAccountIDA         = "10"
		followerAccountIDA          = "11"
		blockedAccountIDA           = "100"
		blockedUserNameA            = "blocked_handle_a"
		blockedDisplayNameA         = "Blocked Name A"
		followingAccountIDB         = "20"
		followerAccountIDB          = "21"
		blockedAccountIDB           = "200"
		blockedUserNameB            = "blocked_handle_b"
		blockedDisplayNameB         = "Blocked Name B"
		expectedStatusCodeOK        = http.StatusOK
		comparisonPollingIterations = 10
		comparisonPollingDelay      = 50 * time.Millisecond
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

	if !resolver.waitForCall(handleResolutionWaitDuration) {
		t.Fatalf("expected handle resolution to be triggered")
	}

	var (
		comparison matrix.ComparisonResult
		resolved   bool
	)

	for attempt := 0; attempt < comparisonPollingIterations; attempt++ {
		recorder = httptest.NewRecorder()
		request = httptest.NewRequest(http.MethodGet, "/", nil)
		router.ServeHTTP(recorder, request)
		if recorder.Code != expectedStatusCodeOK {
			t.Fatalf("expected status %d, got %d", expectedStatusCodeOK, recorder.Code)
		}
		comparison = service.LatestComparison()

		blockedRecordA, foundA := findAccountRecordByID(comparison.OwnerABlockedAll, blockedAccountIDA)
		blockedRecordB, foundB := findAccountRecordByID(comparison.OwnerBBlockedAll, blockedAccountIDB)
		if foundA && foundB &&
			blockedRecordA.DisplayName == blockedDisplayNameA && blockedRecordA.UserName == blockedUserNameA &&
			blockedRecordB.DisplayName == blockedDisplayNameB && blockedRecordB.UserName == blockedUserNameB {
			resolved = true
			break
		}
		time.Sleep(comparisonPollingDelay)
	}

	if !resolved {
		t.Fatalf("blocked accounts did not resolve to handles")
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
