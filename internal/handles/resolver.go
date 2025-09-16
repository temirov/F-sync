package handles

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"
)

const (
	defaultBaseURLString            = "https://twitter.com"
	userPathFormat                  = "/i/user/%s"
	locationHeaderName              = "Location"
	titleStartTag                   = "<title>"
	titleEndTag                     = "</title>"
	titleHandleDelimiter            = "(@"
	titleSuffixDelimiter            = " / "
	whitespaceCharacters            = " \t\r\n"
	errMessageEmptyAccountID        = "account id cannot be empty"
	errMessageMissingLocationHeader = "twitter profile redirect did not include a location header"
	errMessageNoRedirect            = "twitter profile request did not return a redirect"
	errMessageUnexpectedStatus      = "twitter profile request returned unexpected status code"
	errMessageMissingHandle         = "twitter profile redirect did not contain a handle"
	defaultUserAgentHeader          = "User-Agent"
	defaultUserAgentValue           = "F-Sync-HandleResolver/1.0"
	maxProfileHTMLBytes             = 128 * 1024
	defaultDialTimeout              = 5 * time.Second
	defaultTLSHandshakeTimeout      = 5 * time.Second
	defaultResponseHeaderTimeout    = 10 * time.Second
	defaultHTTPTimeout              = 15 * time.Second
	defaultWorkerConcurrency        = 8
)

var (
	errEmptyAccountID        = errors.New(errMessageEmptyAccountID)
	errMissingLocationHeader = errors.New(errMessageMissingLocationHeader)
	errNoRedirect            = errors.New(errMessageNoRedirect)
	errMissingHandle         = errors.New(errMessageMissingHandle)
)

// AccountRecord captures the resolved handle information for a Twitter account.
type AccountRecord struct {
	AccountID   string
	UserName    string
	DisplayName string
}

// Result represents the outcome of a resolve attempt.
type Result struct {
	Record AccountRecord
	Err    error
}

// Config customizes a Resolver instance.
type Config struct {
	BaseURL       string
	Client        *http.Client
	MaxConcurrent int
}

// Resolver resolves Twitter handles for numeric account identifiers.
type Resolver struct {
	client      *http.Client
	baseURL     *url.URL
	workerCount int
	cache       map[string]cacheEntry
	cacheMutex  sync.RWMutex
	flightGroup singleflight.Group
}

type cacheEntry struct {
	record AccountRecord
	err    error
}

// NewResolver constructs a Resolver with sensible defaults for HTTP timeouts and redirect handling.
func NewResolver(configuration Config) (*Resolver, error) {
	baseURLString := configuration.BaseURL
	if strings.TrimSpace(baseURLString) == "" {
		baseURLString = defaultBaseURLString
	}
	parsedBaseURL, err := url.Parse(baseURLString)
	if err != nil {
		return nil, fmt.Errorf("parse base url: %w", err)
	}

	httpClient := configuration.Client
	if httpClient == nil {
		httpClient = newHTTPClient()
	} else {
		clonedClient := *httpClient
		if clonedClient.Transport == nil {
			clonedClient.Transport = defaultTransport()
		}
		clonedClient.CheckRedirect = preventRedirectFollowing
		httpClient = &clonedClient
	}
	if httpClient.Timeout == 0 {
		httpClient.Timeout = defaultHTTPTimeout
	}

	workerCount := configuration.MaxConcurrent
	if workerCount <= 0 {
		workerCount = defaultWorkerConcurrency
	}

	resolver := &Resolver{
		client:      httpClient,
		baseURL:     parsedBaseURL,
		workerCount: workerCount,
		cache:       make(map[string]cacheEntry),
	}
	return resolver, nil
}

// ResolveMany resolves a batch of account identifiers using a bounded worker pool.
func (resolver *Resolver) ResolveMany(ctx context.Context, accountIDs []string) map[string]Result {
	uniqueAccountIDs := resolver.uniqueIDs(accountIDs)
	results := make(map[string]Result, len(uniqueAccountIDs))
	if len(uniqueAccountIDs) == 0 {
		return results
	}

	var (
		resultsMutex sync.Mutex
		group        errgroup.Group
	)
	group.SetLimit(resolver.workerCount)
	for _, accountID := range uniqueAccountIDs {
		accountID := accountID
		group.Go(func() error {
			record, err := resolver.ResolveAccount(ctx, accountID)
			resultsMutex.Lock()
			results[accountID] = Result{Record: record, Err: err}
			resultsMutex.Unlock()
			return nil
		})
	}
	_ = group.Wait()
	return results
}

// ResolveAccount resolves a single numeric account identifier into handle metadata.
func (resolver *Resolver) ResolveAccount(ctx context.Context, accountID string) (AccountRecord, error) {
	if strings.TrimSpace(accountID) == "" {
		return AccountRecord{}, errEmptyAccountID
	}

	resolver.cacheMutex.RLock()
	if entry, ok := resolver.cache[accountID]; ok {
		resolver.cacheMutex.RUnlock()
		return entry.record, entry.err
	}
	resolver.cacheMutex.RUnlock()

	resultChannel := resolver.flightGroup.DoChan(accountID, func() (interface{}, error) {
		record, fetchErr := resolver.fetchAccount(ctx, accountID)
		resolver.cacheMutex.Lock()
		resolver.cache[accountID] = cacheEntry{record: record, err: fetchErr}
		resolver.cacheMutex.Unlock()
		if fetchErr != nil {
			return record, fetchErr
		}
		return record, nil
	})

	select {
	case <-ctx.Done():
		return AccountRecord{}, ctx.Err()
	case result := <-resultChannel:
		if result.Err != nil {
			return AccountRecord{}, result.Err
		}
		record, _ := result.Val.(AccountRecord)
		return record, nil
	}
}

func (resolver *Resolver) fetchAccount(ctx context.Context, accountID string) (AccountRecord, error) {
	profile := AccountRecord{AccountID: accountID}
	redirectURL, err := resolver.resolveRedirect(ctx, accountID)
	if err != nil {
		return profile, err
	}

	handle, err := resolver.extractHandle(redirectURL)
	if err != nil {
		return profile, err
	}
	profile.UserName = handle

	displayName, displayErr := resolver.fetchDisplayName(ctx, redirectURL)
	if displayErr == nil && strings.TrimSpace(displayName) != "" {
		profile.DisplayName = displayName
	}
	return profile, nil
}

func (resolver *Resolver) resolveRedirect(ctx context.Context, accountID string) (string, error) {
	profileURL := resolver.baseURL.ResolveReference(&url.URL{Path: fmt.Sprintf(userPathFormat, accountID)}).String()
	redirectURL, err := resolver.requestRedirect(ctx, http.MethodHead, profileURL)
	if err != nil {
		if !errors.Is(err, errNoRedirect) && !errors.Is(err, errMissingLocationHeader) {
			return "", err
		}
	}
	if redirectURL != "" {
		return redirectURL, nil
	}
	return resolver.requestRedirect(ctx, http.MethodGet, profileURL)
}

func (resolver *Resolver) requestRedirect(ctx context.Context, method string, requestURL string) (string, error) {
	httpRequest, err := http.NewRequestWithContext(ctx, method, requestURL, nil)
	if err != nil {
		return "", err
	}
	httpRequest.Header.Set(defaultUserAgentHeader, defaultUserAgentValue)

	httpResponse, err := resolver.client.Do(httpRequest)
	if err != nil {
		return "", err
	}
	defer func() {
		io.Copy(io.Discard, io.LimitReader(httpResponse.Body, 1024))
		httpResponse.Body.Close()
	}()

	if isRedirectStatus(httpResponse.StatusCode) {
		location := httpResponse.Header.Get(locationHeaderName)
		if strings.TrimSpace(location) == "" {
			return "", errMissingLocationHeader
		}
		return location, nil
	}
	if httpResponse.StatusCode == http.StatusMethodNotAllowed {
		return "", errNoRedirect
	}
	if httpResponse.StatusCode >= 200 && httpResponse.StatusCode < 300 {
		return "", errNoRedirect
	}
	return "", fmt.Errorf("%s: %d", errMessageUnexpectedStatus, httpResponse.StatusCode)
}

func (resolver *Resolver) extractHandle(locationValue string) (string, error) {
	parsedLocation, err := url.Parse(locationValue)
	if err != nil {
		return "", err
	}
	path := strings.Trim(parsedLocation.Path, "/")
	if path == "" {
		return "", errMissingHandle
	}
	segments := strings.Split(path, "/")
	handle := segments[len(segments)-1]
	if strings.TrimSpace(handle) == "" {
		return "", errMissingHandle
	}
	return handle, nil
}

func (resolver *Resolver) fetchDisplayName(ctx context.Context, locationValue string) (string, error) {
	profileURL, err := resolver.combineURL(locationValue)
	if err != nil {
		return "", err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, profileURL, nil)
	if err != nil {
		return "", err
	}
	httpRequest.Header.Set(defaultUserAgentHeader, defaultUserAgentValue)

	httpResponse, err := resolver.client.Do(httpRequest)
	if err != nil {
		return "", err
	}
	defer httpResponse.Body.Close()

	if httpResponse.StatusCode < 200 || httpResponse.StatusCode >= 300 {
		return "", fmt.Errorf("%s: %d", errMessageUnexpectedStatus, httpResponse.StatusCode)
	}
	limitedReader := io.LimitReader(httpResponse.Body, maxProfileHTMLBytes)
	htmlBytes, err := io.ReadAll(limitedReader)
	if err != nil {
		return "", err
	}
	return parseDisplayName(string(htmlBytes)), nil
}

func (resolver *Resolver) combineURL(locationValue string) (string, error) {
	parsedLocation, err := url.Parse(locationValue)
	if err != nil {
		return "", err
	}
	resolved := resolver.baseURL.ResolveReference(parsedLocation)
	return resolved.String(), nil
}

func parseDisplayName(htmlContent string) string {
	startIndex := strings.Index(htmlContent, titleStartTag)
	if startIndex == -1 {
		return ""
	}
	startIndex += len(titleStartTag)
	endIndex := strings.Index(htmlContent[startIndex:], titleEndTag)
	if endIndex == -1 {
		return ""
	}
	endIndex += startIndex
	titleContent := strings.Trim(htmlContent[startIndex:endIndex], whitespaceCharacters)
	if cutIndex := strings.Index(titleContent, titleSuffixDelimiter); cutIndex >= 0 {
		titleContent = strings.Trim(titleContent[:cutIndex], whitespaceCharacters)
	}
	if cutIndex := strings.Index(titleContent, titleHandleDelimiter); cutIndex >= 0 {
		titleContent = strings.Trim(titleContent[:cutIndex], whitespaceCharacters)
	}
	return titleContent
}

func (resolver *Resolver) uniqueIDs(accountIDs []string) []string {
	unique := make([]string, 0, len(accountIDs))
	seen := make(map[string]struct{}, len(accountIDs))
	for _, accountID := range accountIDs {
		if strings.TrimSpace(accountID) == "" {
			continue
		}
		if _, exists := seen[accountID]; exists {
			continue
		}
		seen[accountID] = struct{}{}
		unique = append(unique, accountID)
	}
	return unique
}

func isRedirectStatus(statusCode int) bool {
	return statusCode >= 300 && statusCode < 400
}

func newHTTPClient() *http.Client {
	return &http.Client{
		Timeout:       defaultHTTPTimeout,
		Transport:     defaultTransport(),
		CheckRedirect: preventRedirectFollowing,
	}
}

func defaultTransport() http.RoundTripper {
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: defaultDialTimeout, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   defaultTLSHandshakeTimeout,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConns:          100,
		MaxConnsPerHost:       100,
		ResponseHeaderTimeout: defaultResponseHeaderTimeout,
	}
}

func preventRedirectFollowing(_ *http.Request, _ []*http.Request) error {
	return http.ErrUseLastResponse
}
