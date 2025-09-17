package server_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/f-sync/fsync/internal/matrix"
	"github.com/f-sync/fsync/internal/server"
)

type comparisonServiceStub struct {
	renderedHTML string
	renderError  error
}

func (stub comparisonServiceStub) BuildComparison(accountSetsA matrix.AccountSets, accountSetsB matrix.AccountSets, ownerA matrix.OwnerIdentity, ownerB matrix.OwnerIdentity) matrix.ComparisonResult {
	return matrix.ComparisonResult{
		AccountSetsA: accountSetsA,
		AccountSetsB: accountSetsB,
		OwnerA:       ownerA,
		OwnerB:       ownerB,
	}
}

func (stub comparisonServiceStub) RenderComparisonPage(matrix.ComparisonResult) (string, error) {
	return stub.renderedHTML, stub.renderError
}

func TestServeComparisonResponses(t *testing.T) {
	const (
		accountIDAlpha                     = "1001"
		ownerAccountIDAlpha                = "2001"
		ownerAccountIDBeta                 = "2002"
		ownerHandleAlpha                   = "owner_alpha"
		ownerHandleBeta                    = "owner_beta"
		ownerDisplayAlpha                  = "Owner Alpha"
		ownerDisplayBeta                   = "Owner Beta"
		placeholderHTML                    = "<html><body>ok</body></html>"
		expectedMissingDataBodySubstring   = "comparison data unavailable"
		expectedRenderFailureBodySubstring = "comparison page rendering failed"
		renderFailureErrorMessage          = "render failure"
	)

	ownerIdentityAlpha := matrix.OwnerIdentity{AccountID: ownerAccountIDAlpha, UserName: ownerHandleAlpha, DisplayName: ownerDisplayAlpha}
	ownerIdentityBeta := matrix.OwnerIdentity{AccountID: ownerAccountIDBeta, UserName: ownerHandleBeta, DisplayName: ownerDisplayBeta}
	populatedComparisonData := &server.ComparisonData{
		AccountSetsA: matrix.AccountSets{
			Followers: map[string]matrix.AccountRecord{
				accountIDAlpha: {AccountID: accountIDAlpha, UserName: ownerHandleAlpha, DisplayName: ownerDisplayAlpha},
			},
			Following: map[string]matrix.AccountRecord{
				accountIDAlpha: {AccountID: accountIDAlpha, UserName: ownerHandleAlpha, DisplayName: ownerDisplayAlpha},
			},
			Muted:   map[string]bool{},
			Blocked: map[string]bool{},
		},
		AccountSetsB: matrix.AccountSets{
			Followers: map[string]matrix.AccountRecord{},
			Following: map[string]matrix.AccountRecord{},
			Muted:     map[string]bool{},
			Blocked:   map[string]bool{},
		},
		OwnerA: ownerIdentityAlpha,
		OwnerB: ownerIdentityBeta,
	}

	testCases := []struct {
		name               string
		data               *server.ComparisonData
		service            server.ComparisonService
		expectedStatusCode int
		expectedBody       string
	}{
		{
			name:               "success returns html",
			data:               populatedComparisonData,
			service:            comparisonServiceStub{renderedHTML: placeholderHTML},
			expectedStatusCode: http.StatusOK,
			expectedBody:       placeholderHTML,
		},
		{
			name:               "missing data returns server error",
			data:               nil,
			service:            comparisonServiceStub{renderedHTML: placeholderHTML},
			expectedStatusCode: http.StatusInternalServerError,
			expectedBody:       expectedMissingDataBodySubstring,
		},
		{
			name: "render failure returns error",
			data: populatedComparisonData,
			service: comparisonServiceStub{
				renderedHTML: "",
				renderError:  errors.New(renderFailureErrorMessage),
			},
			expectedStatusCode: http.StatusInternalServerError,
			expectedBody:       expectedRenderFailureBodySubstring,
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			router, err := server.NewRouter(server.RouterConfig{ComparisonData: testCase.data, Service: testCase.service})
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
		})
	}
}

func TestRouterIntegrationRendersPage(t *testing.T) {
	const (
		ownerAccountIDAlpha = "3001"
		ownerAccountIDBeta  = "3002"
		ownerHandleAlpha    = "owner_a"
		ownerHandleBeta     = "owner_b"
		ownerDisplayAlpha   = "Owner A"
		ownerDisplayBeta    = "Owner B"
		friendAccountID     = "4001"
		friendHandle        = "friend_account"
		friendDisplay       = "Friend Account"
		expectedTitleText   = "<title>"
		expectedSectionText = "Overview"
		expectedFriendText  = "Friend Account"
	)

	comparisonData := &server.ComparisonData{
		AccountSetsA: matrix.AccountSets{
			Followers: map[string]matrix.AccountRecord{
				friendAccountID: {AccountID: friendAccountID, UserName: friendHandle, DisplayName: friendDisplay},
			},
			Following: map[string]matrix.AccountRecord{
				friendAccountID: {AccountID: friendAccountID, UserName: friendHandle, DisplayName: friendDisplay},
			},
			Muted: map[string]bool{
				friendAccountID: true,
			},
			Blocked: map[string]bool{},
		},
		AccountSetsB: matrix.AccountSets{
			Followers: map[string]matrix.AccountRecord{},
			Following: map[string]matrix.AccountRecord{},
			Muted:     map[string]bool{},
			Blocked:   map[string]bool{},
		},
		OwnerA: matrix.OwnerIdentity{AccountID: ownerAccountIDAlpha, UserName: ownerHandleAlpha, DisplayName: ownerDisplayAlpha},
		OwnerB: matrix.OwnerIdentity{AccountID: ownerAccountIDBeta, UserName: ownerHandleBeta, DisplayName: ownerDisplayBeta},
	}

	router, err := server.NewRouter(server.RouterConfig{ComparisonData: comparisonData, Service: server.MatrixComparisonService{}})
	if err != nil {
		t.Fatalf("NewRouter returned error: %v", err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, recorder.Code)
	}
	body := recorder.Body.String()
	for _, expectedSnippet := range []string{expectedTitleText, expectedSectionText, expectedFriendText} {
		if !strings.Contains(body, expectedSnippet) {
			t.Fatalf("expected body to contain %q", expectedSnippet)
		}
	}
}
