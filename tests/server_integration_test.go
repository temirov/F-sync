package tests

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/f-sync/fsync/internal/handles"
	"github.com/f-sync/fsync/internal/server"
)

const (
	serverIntegrationFlagName                        = "server_integration"
	serverIntegrationFlagDescription                 = "enable live server integration test with Chrome handle resolution"
	serverIntegrationFlagDisabledMessage             = "server integration test skipped because the flag is disabled"
	serverIntegrationChromeUnavailableMessage        = "server integration test skipped because no Chrome binary is available"
	serverIntegrationRouterErrorFormat               = "server.NewRouter returned error: %v"
	serverIntegrationResolverErrorFormat             = "handles.NewResolver returned error: %v"
	serverIntegrationRequestErrorFormat              = "HTTP %s %s failed: %v"
	serverIntegrationUnexpectedStatusWithBodyFormat  = "unexpected status for %s %s: %d - %s"
	serverIntegrationDecodeErrorFormat               = "decode %s response: %v"
	serverIntegrationResponseReadErrorFormat         = "read %s response body: %v"
	serverIntegrationTaskIncompleteFormat            = "comparison task %s did not complete before timeout"
	serverIntegrationComparisonHTMLMissingFormat     = "expected comparison HTML to contain %q; got %s"
	serverIntegrationComparePath                     = "/api/compare"
	serverIntegrationCompareStatusPathFormat         = "/api/compare/%s"
	serverIntegrationUploadsPath                     = "/api/uploads"
	serverIntegrationComparisonPath                  = "/"
	serverIntegrationHandleLabelFormat               = "%s (@%s)"
	serverIntegrationCompletedStatus                 = "completed"
	serverIntegrationScenarioNameElon                = "resolve elon musk via HTTP API"
	serverIntegrationOwnerAccountIDA                 = "700000001"
	serverIntegrationOwnerAccountIDB                 = "700000002"
	serverIntegrationOwnerHandleA                    = "server_integration_owner_a"
	serverIntegrationOwnerHandleB                    = "server_integration_owner_b"
	serverIntegrationOwnerDisplayNameA               = "Server Integration Owner A"
	serverIntegrationOwnerDisplayNameB               = "Server Integration Owner B"
	serverIntegrationManifestTemplate                = `{"userInfo":{"accountId":"%s","userName":"%s","displayName":"%s"}}`
	serverIntegrationFollowingEntryTemplate          = `{"following":{"accountId":"%s"}}`
	serverIntegrationFollowingArrayFormat            = "[%s]"
	serverIntegrationManifestFileName                = "manifest.js"
	serverIntegrationFollowingFileName               = "following.js"
	serverIntegrationCommaSeparator                  = ","
	serverIntegrationHTTPTimeout                     = 30 * time.Second
	serverIntegrationTaskTimeout                     = 2 * time.Minute
	serverIntegrationTaskPollInterval                = 2 * time.Second
	serverIntegrationParseURLErrorFormat             = "parse test server URL: %v"
	serverIntegrationCreateRequestErrorFormat        = "create %s request for %s: %v"
	serverIntegrationMissingTaskIDMessage            = "task identifier missing in response"
	serverIntegrationUnexpectedResolutionErrorFormat = "expected no resolution errors, got %v"
	serverIntegrationUnexpectedReadyStateFormat      = "expected comparisonReady=%t after %s archive upload; got %t"
	serverIntegrationFirstUploadLabel                = "first"
	serverIntegrationSecondUploadLabel               = "second"
)

// serverIntegrationRunFlag enables TestServerHandleResolutionIntegration, which performs live handle resolution
// using Chrome and external network access. The test is skipped unless the flag is explicitly enabled.
var serverIntegrationRunFlag = flag.Bool(serverIntegrationFlagName, false, serverIntegrationFlagDescription)

type serverIntegrationTestScenario struct {
	scenarioName    string
	targetAccountID string
	expectedHandle  string
	expectedDisplay string
}

type serverIntegrationClient struct {
	testingT   *testing.T
	httpClient *http.Client
	baseURL    *url.URL
}

type serverIntegrationUploadResponse struct {
	ComparisonReady bool `json:"comparisonReady"`
}

func newServerIntegrationClient(testingT *testing.T, baseAddress string) serverIntegrationClient {
	testingT.Helper()
	parsedURL, err := url.Parse(baseAddress)
	if err != nil {
		testingT.Fatalf(serverIntegrationParseURLErrorFormat, err)
	}
	return serverIntegrationClient{
		testingT:   testingT,
		httpClient: &http.Client{Timeout: serverIntegrationHTTPTimeout},
		baseURL:    parsedURL,
	}
}

func (client serverIntegrationClient) uploadArchive(archivePath string) bool {
	client.testingT.Helper()
	request := newUploadRequest(client.testingT, archivePath)
	request.URL.Path = serverIntegrationUploadsPath
	request.URL.RawPath = serverIntegrationUploadsPath
	client.prepareRequest(request)
	response, err := client.httpClient.Do(request)
	if err != nil {
		client.testingT.Fatalf(serverIntegrationRequestErrorFormat, request.Method, request.URL.String(), err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		bodyBytes, readErr := io.ReadAll(response.Body)
		if readErr != nil {
			client.testingT.Fatalf(serverIntegrationResponseReadErrorFormat, request.URL.String(), readErr)
		}
		client.testingT.Fatalf(serverIntegrationUnexpectedStatusWithBodyFormat, request.Method, request.URL.String(), response.StatusCode, string(bodyBytes))
	}
	var upload serverIntegrationUploadResponse
	if err := json.NewDecoder(response.Body).Decode(&upload); err != nil {
		client.testingT.Fatalf(serverIntegrationDecodeErrorFormat, request.URL.String(), err)
	}
	return upload.ComparisonReady
}

func (client serverIntegrationClient) prepareRequest(request *http.Request) {
	requestURL := client.baseURL.ResolveReference(&url.URL{Path: request.URL.Path, RawQuery: request.URL.RawQuery})
	request.URL = requestURL
	request.Host = client.baseURL.Host
	request.RequestURI = ""
}

func (client serverIntegrationClient) startComparisonTask() comparisonTaskResponse {
	client.testingT.Helper()
	request := client.newRequest(http.MethodPost, serverIntegrationComparePath, nil)
	response, err := client.httpClient.Do(request)
	if err != nil {
		client.testingT.Fatalf(serverIntegrationRequestErrorFormat, request.Method, request.URL.String(), err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		bodyBytes, readErr := io.ReadAll(response.Body)
		if readErr != nil {
			client.testingT.Fatalf(serverIntegrationResponseReadErrorFormat, request.URL.String(), readErr)
		}
		client.testingT.Fatalf(serverIntegrationUnexpectedStatusWithBodyFormat, request.Method, request.URL.String(), response.StatusCode, string(bodyBytes))
	}
	var task comparisonTaskResponse
	if err := json.NewDecoder(response.Body).Decode(&task); err != nil {
		client.testingT.Fatalf(serverIntegrationDecodeErrorFormat, request.URL.String(), err)
	}
	if strings.TrimSpace(task.TaskID) == "" {
		client.testingT.Fatal(serverIntegrationMissingTaskIDMessage)
	}
	return task
}

func (client serverIntegrationClient) waitForTaskCompletion(taskID string) {
	client.testingT.Helper()
	deadline := time.Now().Add(serverIntegrationTaskTimeout)
	for time.Now().Before(deadline) {
		request := client.newRequest(http.MethodGet, fmt.Sprintf(serverIntegrationCompareStatusPathFormat, taskID), nil)
		response, err := client.httpClient.Do(request)
		if err != nil {
			client.testingT.Fatalf(serverIntegrationRequestErrorFormat, request.Method, request.URL.String(), err)
		}
		progress := func() comparisonProgressResponse {
			defer response.Body.Close()
			if response.StatusCode != http.StatusOK {
				bodyBytes, readErr := io.ReadAll(response.Body)
				if readErr != nil {
					client.testingT.Fatalf(serverIntegrationResponseReadErrorFormat, request.URL.String(), readErr)
				}
				client.testingT.Fatalf(serverIntegrationUnexpectedStatusWithBodyFormat, request.Method, request.URL.String(), response.StatusCode, string(bodyBytes))
			}
			var progress comparisonProgressResponse
			if err := json.NewDecoder(response.Body).Decode(&progress); err != nil {
				client.testingT.Fatalf(serverIntegrationDecodeErrorFormat, request.URL.String(), err)
			}
			return progress
		}()
		if progress.Status == serverIntegrationCompletedStatus {
			if len(progress.Errors) != 0 {
				client.testingT.Fatalf(serverIntegrationUnexpectedResolutionErrorFormat, progress.Errors)
			}
			return
		}
		time.Sleep(serverIntegrationTaskPollInterval)
	}
	client.testingT.Fatalf(serverIntegrationTaskIncompleteFormat, taskID)
}

func (client serverIntegrationClient) fetchComparisonPage() string {
	client.testingT.Helper()
	request := client.newRequest(http.MethodGet, serverIntegrationComparisonPath, nil)
	response, err := client.httpClient.Do(request)
	if err != nil {
		client.testingT.Fatalf(serverIntegrationRequestErrorFormat, request.Method, request.URL.String(), err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		bodyBytes, readErr := io.ReadAll(response.Body)
		if readErr != nil {
			client.testingT.Fatalf(serverIntegrationResponseReadErrorFormat, request.URL.String(), readErr)
		}
		client.testingT.Fatalf(serverIntegrationUnexpectedStatusWithBodyFormat, request.Method, request.URL.String(), response.StatusCode, string(bodyBytes))
	}
	bodyBytes, err := io.ReadAll(response.Body)
	if err != nil {
		client.testingT.Fatalf(serverIntegrationResponseReadErrorFormat, request.URL.String(), err)
	}
	return string(bodyBytes)
}

func (client serverIntegrationClient) newRequest(method string, relativePath string, body io.Reader) *http.Request {
	client.testingT.Helper()
	requestURL := client.baseURL.ResolveReference(&url.URL{Path: relativePath})
	request, err := http.NewRequest(method, requestURL.String(), body)
	if err != nil {
		client.testingT.Fatalf(serverIntegrationCreateRequestErrorFormat, method, relativePath, err)
	}
	return request
}

func buildServerIntegrationArchive(t *testing.T, ownerAccountID string, ownerHandle string, ownerDisplayName string, followingIDs []string) string {
	t.Helper()
	manifestContent := fmt.Sprintf(serverIntegrationManifestTemplate, ownerAccountID, ownerHandle, ownerDisplayName)
	followingEntries := make([]string, 0, len(followingIDs))
	for _, accountID := range followingIDs {
		followingEntries = append(followingEntries, fmt.Sprintf(serverIntegrationFollowingEntryTemplate, accountID))
	}
	followingContent := fmt.Sprintf(serverIntegrationFollowingArrayFormat, strings.Join(followingEntries, serverIntegrationCommaSeparator))
	return createArchive(t, map[string]string{
		serverIntegrationManifestFileName:  manifestContent,
		serverIntegrationFollowingFileName: followingContent,
	})
}

func TestServerHandleResolutionIntegration(t *testing.T) {
	if !*serverIntegrationRunFlag {
		t.Skip(serverIntegrationFlagDisabledMessage)
	}

	chromePath, chromeErr := resolveChromeBinaryPathForIntegration()
	if chromeErr != nil {
		t.Skipf(integrationSkipMessageFormat, serverIntegrationChromeUnavailableMessage, chromeErr)
	}

	resolver, err := handles.NewResolver(handles.Config{ChromeBinaryPath: chromePath})
	if err != nil {
		t.Fatalf(serverIntegrationResolverErrorFormat, err)
	}

	router, err := server.NewRouter(server.RouterConfig{HandleResolver: resolver})
	if err != nil {
		t.Fatalf(serverIntegrationRouterErrorFormat, err)
	}

	testServer := httptest.NewServer(router)
	defer testServer.Close()

	scenarios := []serverIntegrationTestScenario{
		{
			scenarioName:    serverIntegrationScenarioNameElon,
			targetAccountID: integrationAccountIDElon,
			expectedHandle:  integrationExpectedHandleElon,
			expectedDisplay: integrationExpectedDisplayNameElon,
		},
	}

	for _, scenario := range scenarios {
		scenario := scenario
		t.Run(scenario.scenarioName, func(t *testing.T) {
			client := newServerIntegrationClient(t, testServer.URL)

			archiveA := buildServerIntegrationArchive(t, serverIntegrationOwnerAccountIDA, serverIntegrationOwnerHandleA, serverIntegrationOwnerDisplayNameA, []string{scenario.targetAccountID})
			archiveB := buildServerIntegrationArchive(t, serverIntegrationOwnerAccountIDB, serverIntegrationOwnerHandleB, serverIntegrationOwnerDisplayNameB, []string{scenario.targetAccountID})

			firstReady := client.uploadArchive(archiveA)
			if firstReady {
				t.Fatalf(serverIntegrationUnexpectedReadyStateFormat, false, serverIntegrationFirstUploadLabel, firstReady)
			}
			secondReady := client.uploadArchive(archiveB)
			if !secondReady {
				t.Fatalf(serverIntegrationUnexpectedReadyStateFormat, true, serverIntegrationSecondUploadLabel, secondReady)
			}

			task := client.startComparisonTask()
			client.waitForTaskCompletion(task.TaskID)

			comparisonHTML := client.fetchComparisonPage()
			expectedLabel := fmt.Sprintf(serverIntegrationHandleLabelFormat, scenario.expectedDisplay, scenario.expectedHandle)
			if !strings.Contains(comparisonHTML, expectedLabel) {
				t.Fatalf(serverIntegrationComparisonHTMLMissingFormat, expectedLabel, comparisonHTML)
			}
		})
	}
}
