package handles

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"

	"github.com/f-sync/fsync/internal/intentparser"
)

const (
	defaultIntentBaseURLString = "https://x.com"
	intentPathFormat           = "/intent/user?user_id=%s"
	errMessageEmptyAccountID   = "account id cannot be empty"
	errMessageEmptyIntentHTML  = "twitter intent page did not return any HTML"
	defaultWorkerConcurrency   = 1
)

var (
	errEmptyAccountID  = errors.New(errMessageEmptyAccountID)
	errEmptyIntentHTML = errors.New(errMessageEmptyIntentHTML)

	globalAccountCache      = newAccountCache()
	globalAccountFetchGroup singleflight.Group
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
	BaseURL                 string
	IntentFetcher           IntentFetcher
	ChromeBinaryPath        string
	ChromeUserAgent         string
	ChromeVirtualTimeBudget time.Duration
	ChromeRequestDelay      time.Duration
	MaxConcurrent           int
}

// Resolver resolves Twitter handles for numeric account identifiers.
type Resolver struct {
	baseURL       *url.URL
	workerCount   int
	intentFetcher IntentFetcher
	accountCache  *accountCache
	fetchGroup    *singleflight.Group
}

type accountCacheEntry struct {
	record AccountRecord
	err    error
}

// accountCache provides concurrency-safe storage for resolved account data.
type accountCache struct {
	entries map[string]accountCacheEntry
	mutex   sync.RWMutex
}

// NewResolver constructs a Resolver with sensible defaults for intent lookups.
func NewResolver(configuration Config) (*Resolver, error) {
	baseURLString := strings.TrimSpace(configuration.BaseURL)
	if baseURLString == "" {
		baseURLString = defaultIntentBaseURLString
	}
	parsedBaseURL, parseErr := url.Parse(baseURLString)
	if parseErr != nil {
		return nil, fmt.Errorf("parse base url: %w", parseErr)
	}

	workerCount := configuration.MaxConcurrent
	if workerCount <= 0 {
		workerCount = defaultWorkerConcurrency
	}

	intentFetcher := configuration.IntentFetcher
	if intentFetcher == nil {
		chromeBinaryPath := resolveChromeBinaryPath(configuration)
		chromeFetcher, chromeErr := NewChromeIntentFetcher(ChromeFetcherConfig{
			BinaryPath:        chromeBinaryPath,
			UserAgent:         configuration.ChromeUserAgent,
			VirtualTimeBudget: configuration.ChromeVirtualTimeBudget,
			RequestDelay:      configuration.ChromeRequestDelay,
		})
		if chromeErr != nil {
			return nil, chromeErr
		}
		intentFetcher = chromeFetcher
	}

	resolver := &Resolver{
		baseURL:       parsedBaseURL,
		workerCount:   workerCount,
		intentFetcher: intentFetcher,
		accountCache:  globalAccountCache,
		fetchGroup:    &globalAccountFetchGroup,
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
			record, resolveErr := resolver.ResolveAccount(ctx, accountID)
			resultsMutex.Lock()
			results[accountID] = Result{Record: record, Err: resolveErr}
			resultsMutex.Unlock()
			return nil
		})
	}
	_ = group.Wait()
	return results
}

// ResolveAccount resolves a single numeric account identifier into handle metadata.
func (resolver *Resolver) ResolveAccount(ctx context.Context, accountID string) (AccountRecord, error) {
	normalizedAccountID := strings.TrimSpace(accountID)
	if normalizedAccountID == "" {
		return AccountRecord{}, errEmptyAccountID
	}

	if cachedEntry, found := resolver.accountCache.Lookup(normalizedAccountID); found {
		return cachedEntry.record, cachedEntry.err
	}

	resultChannel := resolver.fetchGroup.DoChan(normalizedAccountID, func() (interface{}, error) {
		record, fetchErr := resolver.fetchAccount(ctx, normalizedAccountID)
		resolver.accountCache.Store(normalizedAccountID, record, fetchErr)
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
	accountRecord := AccountRecord{AccountID: accountID}
	intentRequest := IntentRequest{AccountID: accountID, URL: resolver.intentURL(accountID)}
	intentPage, fetchErr := resolver.intentFetcher.FetchIntentPage(ctx, intentRequest)
	if fetchErr != nil {
		return accountRecord, fetchErr
	}

	htmlParser := intentparser.NewIntentHTMLParser(intentPage.HTML)
	handle, handleErr := htmlParser.ExtractHandle()
	if handleErr != nil {
		return accountRecord, handleErr
	}
	accountRecord.UserName = handle

	displayName := htmlParser.ExtractDisplayName(handle)
	if strings.TrimSpace(displayName) != "" {
		accountRecord.DisplayName = displayName
	}
	return accountRecord, nil
}

func (resolver *Resolver) intentURL(accountID string) string {
	return resolver.baseURL.ResolveReference(&url.URL{Path: fmt.Sprintf(intentPathFormat, accountID)}).String()
}

func (resolver *Resolver) uniqueIDs(accountIDs []string) []string {
	unique := make([]string, 0, len(accountIDs))
	seen := make(map[string]struct{}, len(accountIDs))
	for _, accountID := range accountIDs {
		trimmed := strings.TrimSpace(accountID)
		if trimmed == "" {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		unique = append(unique, trimmed)
	}
	return unique
}

// newAccountCache initializes an empty accountCache instance.
func newAccountCache() *accountCache {
	return &accountCache{entries: make(map[string]accountCacheEntry)}
}

// Lookup retrieves the cached entry for the supplied account identifier.
func (cache *accountCache) Lookup(accountID string) (accountCacheEntry, bool) {
	cache.mutex.RLock()
	defer cache.mutex.RUnlock()
	entry, found := cache.entries[accountID]
	return entry, found
}

// Store saves the provided account record and error result for reuse.
func (cache *accountCache) Store(accountID string, record AccountRecord, resolveErr error) {
	cache.mutex.Lock()
	cache.entries[accountID] = accountCacheEntry{record: record, err: resolveErr}
	cache.mutex.Unlock()
}
