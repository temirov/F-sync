package cresolver

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/f-sync/fsync/internal/handles"
	"github.com/f-sync/fsync/internal/matrix"
)

const (
	defaultIntentBaseURL     = "https://x.com"
	intentPathFormat         = "/intent/user?user_id=%s"
	errMessageCreateResolver = "create account resolver"
)

// Config configures a Service instance.
type Config struct {
	Handles        handles.Config
	AccountTimeout time.Duration
	RequestPacing  RequestPacingConfig
	IntentBaseURL  string
	Resolver       AccountResolver
}

// RequestPacingConfig describes the pacing characteristics for handle resolution requests.
type RequestPacingConfig struct {
	BaseDelay       time.Duration
	Jitter          time.Duration
	BurstSize       int
	BurstRest       time.Duration
	BurstRestJitter time.Duration
	RandomGenerator *rand.Rand
}

// Request captures the account identifiers to resolve.
type Request struct {
	AccountIDs []string
}

// AccountResolver resolves individual account identifiers.
type AccountResolver interface {
	ResolveAccount(ctx context.Context, accountID string) (handles.AccountRecord, error)
}

// AccountResolution represents the resolution outcome for a single identifier.
type AccountResolution struct {
	AccountID string
	IntentURL string
	Record    handles.AccountRecord
	Err       error
}

// Service resolves numeric account identifiers into handle information.
type Service struct {
	resolver       AccountResolver
	requestPacer   *requestPacer
	accountTimeout time.Duration
	intentBaseURL  string
}

var _ matrix.AccountHandleResolver = (*Service)(nil)

// NewService constructs a Service from configuration values.
func NewService(configuration Config) (*Service, error) {
	resolver := configuration.Resolver
	if resolver == nil {
		handlesResolver, resolverErr := handles.NewResolver(configuration.Handles)
		if resolverErr != nil {
			return nil, fmt.Errorf("%s: %w", errMessageCreateResolver, resolverErr)
		}
		resolver = handlesResolver
	}

	service := &Service{
		resolver:       resolver,
		requestPacer:   newRequestPacer(configuration.RequestPacing),
		accountTimeout: configuration.AccountTimeout,
		intentBaseURL:  normalizeBaseURL(configuration.IntentBaseURL),
	}
	return service, nil
}

// ResolveBatch resolves the supplied account identifiers in order and returns their outcomes.
func (service *Service) ResolveBatch(ctx context.Context, request Request) ([]AccountResolution, error) {
	normalizedAccountIDs := service.normalizeAccountIDs(request.AccountIDs)
	return service.resolveAccountIDs(ctx, normalizedAccountIDs)
}

// ResolveMany resolves the provided identifiers and satisfies the matrix.AccountHandleResolver interface.
func (service *Service) ResolveMany(ctx context.Context, accountIDs []string) map[string]handles.Result {
	normalizedAccountIDs := service.normalizeAccountIDs(accountIDs)
	resolutions, resolveErr := service.resolveAccountIDs(ctx, normalizedAccountIDs)

	results := make(map[string]handles.Result, len(normalizedAccountIDs))
	for index, accountID := range normalizedAccountIDs {
		if index < len(resolutions) {
			resolution := resolutions[index]
			results[accountID] = handles.Result{Record: resolution.Record, Err: resolution.Err}
			continue
		}
		if resolveErr != nil {
			results[accountID] = handles.Result{Err: resolveErr}
		}
	}
	return results
}

func (service *Service) resolveAccountIDs(ctx context.Context, accountIDs []string) ([]AccountResolution, error) {
	resolutions := make([]AccountResolution, 0, len(accountIDs))
	if len(accountIDs) == 0 {
		return resolutions, nil
	}

	for index, accountID := range accountIDs {
		select {
		case <-ctx.Done():
			return resolutions, ctx.Err()
		default:
		}

		resolutions = append(resolutions, service.resolveSingleAccount(ctx, accountID))

		if index == len(accountIDs)-1 {
			continue
		}
		if waitErr := service.waitForNext(ctx); waitErr != nil {
			return resolutions, waitErr
		}
	}

	return resolutions, nil
}

func (service *Service) resolveSingleAccount(ctx context.Context, accountID string) AccountResolution {
	resolution := AccountResolution{
		AccountID: accountID,
		IntentURL: service.intentURL(accountID),
	}

	accountCtx := ctx
	cancelAccount := func() {}
	if service.accountTimeout > 0 {
		var cancel context.CancelFunc
		accountCtx, cancel = context.WithTimeout(ctx, service.accountTimeout)
		cancelAccount = cancel
	}
	record, resolveErr := service.resolver.ResolveAccount(accountCtx, accountID)
	cancelAccount()

	resolution.Record = record
	resolution.Err = resolveErr
	return resolution
}

func (service *Service) waitForNext(ctx context.Context) error {
	if service.requestPacer == nil {
		return nil
	}

	delayDuration, restDuration := service.requestPacer.NextWaits()
	if err := waitForDuration(ctx, delayDuration); err != nil {
		return err
	}
	if err := waitForDuration(ctx, restDuration); err != nil {
		return err
	}
	return nil
}

func (service *Service) intentURL(accountID string) string {
	return service.intentBaseURL + fmt.Sprintf(intentPathFormat, accountID)
}

func (service *Service) normalizeAccountIDs(accountIDs []string) []string {
	normalized := make([]string, 0, len(accountIDs))
	for _, accountID := range accountIDs {
		trimmed := strings.TrimSpace(accountID)
		if trimmed == "" {
			continue
		}
		normalized = append(normalized, trimmed)
	}
	return normalized
}

func waitForDuration(ctx context.Context, duration time.Duration) error {
	if duration <= 0 {
		return nil
	}

	timer := time.NewTimer(duration)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func normalizeBaseURL(intentBaseURL string) string {
	trimmed := strings.TrimSpace(intentBaseURL)
	if trimmed == "" {
		trimmed = defaultIntentBaseURL
	}
	return strings.TrimRight(trimmed, "/")
}

type requestPacer struct {
	baseDelay       time.Duration
	jitter          time.Duration
	burstSize       int
	burstRest       time.Duration
	burstRestJitter time.Duration

	randomGenerator *rand.Rand
	mutex           sync.Mutex
	processed       int
}

func newRequestPacer(configuration RequestPacingConfig) *requestPacer {
	baseDelay := configuration.BaseDelay
	if baseDelay < 0 {
		baseDelay = 0
	}
	burstRest := configuration.BurstRest
	if burstRest < 0 {
		burstRest = 0
	}
	randomGenerator := configuration.RandomGenerator
	if randomGenerator == nil {
		randomGenerator = rand.New(rand.NewSource(time.Now().UnixNano()))
	}

	return &requestPacer{
		baseDelay:       baseDelay,
		jitter:          configuration.Jitter,
		burstSize:       configuration.BurstSize,
		burstRest:       burstRest,
		burstRestJitter: configuration.BurstRestJitter,
		randomGenerator: randomGenerator,
	}
}

func (pacer *requestPacer) NextWaits() (time.Duration, time.Duration) {
	pacer.mutex.Lock()
	defer pacer.mutex.Unlock()

	pacer.processed++

	delayDuration := pacer.sampleDuration(pacer.baseDelay, pacer.jitter)
	var restDuration time.Duration
	if pacer.burstSize > 0 && pacer.processed%pacer.burstSize == 0 {
		restDuration = pacer.sampleDuration(pacer.burstRest, pacer.burstRestJitter)
	}
	return delayDuration, restDuration
}

func (pacer *requestPacer) sampleDuration(baseDuration time.Duration, jitter time.Duration) time.Duration {
	if baseDuration < 0 {
		baseDuration = 0
	}
	if jitter <= 0 {
		return baseDuration
	}

	offset := (pacer.randomGenerator.Float64()*2 - 1) * float64(jitter)
	sampled := time.Duration(float64(baseDuration) + offset)
	if sampled < 0 {
		return 0
	}
	return sampled
}
