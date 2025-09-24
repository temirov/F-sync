// internal/xresolver/service.go
package xresolver

import (
	"context"
	"fmt"
	"math/rand"
	"net"
	neturl "net/url"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

const (
	defaultVirtualTimeBudgetMilliseconds = 15000

	// Headless / GPU
	chromeHeadlessFlagKey           = "headless"
	chromeHeadlessModeNewValue      = "new"
	chromeDisableDevShmUsageFlagKey = "disable-dev-shm-usage"
	chromeEnableGPUFlagKey          = "enable-gpu"

	// Stealth / noise reduction
	chromeEnableAutomationFlagKey                     = "enable-automation"
	chromeDisableBlinkFeaturesFlagKey                 = "disable-blink-features"
	chromeAutomationControlledBlinkValue              = "AutomationControlled"
	chromeDisableExtensionsFlagKey                    = "disable-extensions"
	chromeDisableComponentExtensionsBackgroundFlagKey = "disable-component-extensions-with-background-pages"
	chromeHideScrollbarsFlagKey                       = "hide-scrollbars"
	chromeNoFirstRunFlagKey                           = "no-first-run"
	chromeNoDefaultBrowserCheckFlagKey                = "no-default-browser-check"
	chromeLogLevelFlagKey                             = "log-level"
	chromeSilentFlagKey                               = "silent"
	chromeDisableLoggingFlagKey                       = "disable-logging"
	chromeIgnoreCertificateErrorsFlag                 = "ignore-certificate-errors"

	chromeUserAgentFlagKey         = "user-agent"
	chromeVirtualTimeBudgetFlagKey = "virtual-time-budget"
	chromeProxyServerFlagKey       = "proxy-server"

	httpsProxyEnvironmentUpper = "HTTPS_PROXY"
	httpsProxyEnvironmentLower = "https_proxy"
	httpProxyEnvironmentUpper  = "HTTP_PROXY"
	httpProxyEnvironmentLower  = "http_proxy"
	allProxyEnvironmentUpper   = "ALL_PROXY"
	allProxyEnvironmentLower   = "all_proxy"
	noProxyEnvironmentUpper    = "NO_PROXY"
	noProxyEnvironmentLower    = "no_proxy"

	chromeSilentLogLevelValue            = "3"
	chromeRendererEmptyURLErrorMessage   = "empty url"
	chromeLogNavigationStartMessage      = "chromedp navigate: user-agent=%q url=%s"
	chromeLogNavigationSuccessMessage    = "chromedp render success: url=%s bytes=%d"
	chromeLogNavigationErrorMessage      = "chromedp render failure: url=%s err=%v"
	chromeLogNetworkRequestMessage       = "chromedp network request: url=%s"
	chromeLogNetworkResponseMessage      = "chromedp network response: url=%s status=%d"
	chromeLogNetworkFailureMessage       = "chromedp network failure: url=%s error=%s canceled=%v"
	chromeLogTargetCrashMessage          = "chromedp target crashed"
	acceptLanguageHeaderName             = "Accept-Language"
	acceptLanguageHeaderValue            = "en-US,en;q=0.9"
	upgradeInsecureRequestsHeaderName    = "Upgrade-Insecure-Requests"
	upgradeInsecureRequestsHeaderValue   = "1"
	documentReadyStateScript             = "document.readyState"
	documentReadyStateCompleteValue      = "complete"
	documentReadyStatePollInterval       = 100 * time.Millisecond
	documentOuterHTMLScript              = "document.documentElement.outerHTML"
	documentOuterHTMLNilDestinationError = "html destination pointer is nil"
	navigatorPlatformMacValue            = "MacIntel"
	navigatorPlatformWindowsValue        = "Win32"
	navigatorPlatformLinuxValue          = "Linux x86_64"
	navigatorWebdriverOverrideScript     = "Object.defineProperty(navigator, 'webdriver', { get: () => undefined });"
	navigatorLanguagesOverrideScript     = "Object.defineProperty(navigator, 'languages', { get: () => ['en-US','en'] });"
	navigatorPluginsOverrideScript       = "Object.defineProperty(navigator, 'plugins', { get: () => [1, 2, 3, 4, 5] });"
	windowChromeRuntimeDefinitionScript  = "window.chrome = window.chrome || {}; window.chrome.runtime = {};"
	navigatorPermissionsOverrideScript   = "const originalQuery = window.navigator.permissions.query; window.navigator.permissions.query = (parameters) => (parameters && parameters.name === 'notifications' ? Promise.resolve({ state: 'default' }) : originalQuery(parameters));"
	userAgentChromeMarker                = "Chrome/"
	userAgentMacintoshToken              = "macintosh"
	userAgentWindowsToken                = "windows"
	userAgentLinuxToken                  = "linux"
	userAgentMacVersionToken             = "Mac OS X "
	userAgentWindowsVersionToken         = "Windows NT "
	userAgentTokenUnderscore             = "_"
	userAgentPlatformMacOS               = "macOS"
	userAgentPlatformWindows             = "Windows"
	userAgentPlatformLinux               = "Linux"
	userAgentPlatformVersionDefault      = "0.0.0"
	userAgentArchitectureX86             = "x86"
	userAgentBitness64                   = "64"
	userAgentWow64Token                  = "wow64"
	versionDelimiterSpaceRune            = ' '
	versionDelimiterSemicolonRune        = ';'
	versionDelimiterParenRune            = ')'
	chromeBrandNotABrandName             = "Not A(Brand"
	chromeBrandNotABrandVersion          = "8"
	chromeBrandChromiumName              = "Chromium"
	chromeBrandGoogleChromeName          = "Google Chrome"
)

var stealthScripts = []string{
	navigatorWebdriverOverrideScript,
	windowChromeRuntimeDefinitionScript,
	navigatorLanguagesOverrideScript,
	navigatorPluginsOverrideScript,
	navigatorPermissionsOverrideScript,
}

// Config controls resolver behavior. Suitable for CLI & Web usage.
type Config struct {
	ChromePath          string // path to Chrome/Chromium binary
	VirtualTimeBudgetMS int    // headless Chrome --virtual-time-budget (ms)

	PerIDTimeout   time.Duration // timeout per ID
	AttemptTimeout time.Duration // timeout per single render attempt (<= PerIDTimeout), optional

	// Request pacing (between IDs)
	Delay       time.Duration // base delay between requests
	Jitter      time.Duration // uniform jitter in [-Jitter, +Jitter]
	BurstSize   int           // 0 disables
	BurstRest   time.Duration // rest after each burst
	BurstJitter time.Duration // jitter for BurstRest

	// Robustness / retries (within the same ID)
	Retries  int           // number of additional attempts (0 = single attempt)
	RetryMin time.Duration // min backoff between attempts
	RetryMax time.Duration // max backoff between attempts

	// UA rotation
	UserAgents []string // rotate per request; if empty, DefaultUAs used

	// Optional debug logger; if nil, no logs.
	Logf func(format string, args ...any)
}

// Request is the payload for batch resolution.
type Request struct {
	IDs []string
}

// Profile is the result for a single ID.
type Profile struct {
	ID          string
	Handle      string
	DisplayName string
	FromURL     string
	Err         string // empty if success
}

// Renderer abstracts how HTML is obtained (exec Chrome vs. mock in tests).
type Renderer interface {
	Render(ctx context.Context, userAgent, url string, vtBudgetMS int, chromePath string) (string, error)
}

// ChromeRenderer uses a headless Chrome process.
type ChromeRenderer struct{}

func NewChromeRenderer() *ChromeRenderer { return &ChromeRenderer{} }

func (renderer *ChromeRenderer) Render(ctx context.Context, userAgent, url string, vtBudgetMS int, chromePath string) (string, error) {
	effectiveBudget := vtBudgetMS
	if effectiveBudget <= 0 {
		effectiveBudget = defaultVirtualTimeBudgetMilliseconds
	}

	trimmedURL := strings.TrimSpace(url)
	if trimmedURL == "" {
		return "", fmt.Errorf(chromeRendererEmptyURLErrorMessage)
	}
	trimmedUserAgent := strings.TrimSpace(userAgent)

	// --- Stable allocator flags (no DefaultExecAllocatorOptions) ---
	// We prefer legacy headless + software GL for stability on macOS.
	allocatorOptions := []chromedp.ExecAllocatorOption{
		// Legacy headless (boolean) – avoids renderer crash seen on macOS with headless=new.
		chromedp.Flag(chromeHeadlessFlagKey, true),

		// Disable GPU and use SwiftShader (software GL).
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("use-gl", "swiftshader"),

		// Reduce noise / prompts / logs.
		chromedp.Flag(chromeDisableDevShmUsageFlagKey, true),
		chromedp.Flag(chromeRemoteAllowOriginsFlagKey, chromeRemoteAllowOriginsValue),
		chromedp.Flag(chromeHideScrollbarsFlagKey, true),
		chromedp.Flag(chromeNoFirstRunFlagKey, true),
		chromedp.Flag(chromeNoDefaultBrowserCheckFlagKey, true),
		chromedp.Flag(chromeLogLevelFlagKey, chromeSilentLogLevelValue),
		chromedp.Flag(chromeSilentFlagKey, true),
		chromedp.Flag(chromeDisableLoggingFlagKey, true),
		chromedp.Flag(chromeIgnoreCertificateErrorsFlag, true),
		chromedp.Flag(chromeVirtualTimeBudgetFlagKey, strconv.Itoa(effectiveBudget)),

		// Reduce automation hints.
		chromedp.Flag(chromeEnableAutomationFlagKey, false),
		chromedp.Flag(chromeDisableBlinkFeaturesFlagKey, chromeAutomationControlledBlinkValue),
		chromedp.Flag(chromeDisableExtensionsFlagKey, true),
		chromedp.Flag(chromeDisableComponentExtensionsBackgroundFlagKey, true),
	}

	// Sandbox flags only for Linux containers (not needed on macOS/Windows).
	if runtime.GOOS == "linux" {
		allocatorOptions = append(allocatorOptions,
			chromedp.Flag("no-sandbox", true),
			chromedp.Flag("disable-setuid-sandbox", true),
		)
	}

	// Optional override via env:
	//   XRESOLVER_HEADLESS_MODE=legacy|new
	//   XRESOLVER_HEADLESS_GPU=0|1
	switch strings.ToLower(strings.TrimSpace(os.Getenv("XRESOLVER_HEADLESS_MODE"))) {
	case "new":
		// Switch to headless=new (remove boolean headless, turn GPU back on unless XRESOLVER_HEADLESS_GPU=0).
		allocatorOptions = append(allocatorOptions,
			chromedp.Flag(chromeHeadlessFlagKey, chromeHeadlessModeNewValue),
		)
	}
	if os.Getenv("XRESOLVER_HEADLESS_GPU") == "1" {
		allocatorOptions = append(allocatorOptions,
			chromedp.Flag("disable-gpu", false),
			chromedp.Flag("use-gl", "angle"),
		)
	}

	if proxyValue := chromeProxyServerValue(trimmedURL); proxyValue != "" {
		allocatorOptions = append(allocatorOptions, chromedp.Flag(chromeProxyServerFlagKey, proxyValue))
	}
	if trimmedUserAgent != "" {
		allocatorOptions = append(allocatorOptions, chromedp.Flag(chromeUserAgentFlagKey, trimmedUserAgent))
	}
	if p := strings.TrimSpace(chromePath); p != "" {
		allocatorOptions = append(allocatorOptions, chromedp.ExecPath(p))
	}

	allocatorCtx, cancelAllocator := chromedp.NewExecAllocator(ctx, allocatorOptions...)
	defer cancelAllocator()

	chromeLogPrinter := logFunctionFromContext(ctx)
	contextOptions := []chromedp.ContextOption{}
	if chromeLogPrinter != nil {
		contextOptions = append(contextOptions,
			chromedp.WithLogf(chromeLogPrinter),
			chromedp.WithErrorf(chromeLogPrinter),
			chromedp.WithDebugf(chromeLogPrinter),
		)
	}
	chromeCtx, cancelChrome := chromedp.NewContext(allocatorCtx, contextOptions...)
	defer cancelChrome()

	if chromeLogPrinter != nil {
		requestURLByID := map[network.RequestID]string{}
		var requestMapMutex sync.Mutex
		chromedp.ListenTarget(chromeCtx, func(event any) {
			switch typedEvent := event.(type) {
			case *network.EventRequestWillBeSent:
				requestMapMutex.Lock()
				requestURLByID[typedEvent.RequestID] = typedEvent.Request.URL
				requestMapMutex.Unlock()
				chromeLogPrinter(chromeLogNetworkRequestMessage, typedEvent.Request.URL)
			case *network.EventResponseReceived:
				requestMapMutex.Lock()
				requestURL := requestURLByID[typedEvent.RequestID]
				if requestURL == "" {
					requestURL = typedEvent.Response.URL
				}
				requestMapMutex.Unlock()
				chromeLogPrinter(chromeLogNetworkResponseMessage, requestURL, int(typedEvent.Response.Status))
			case *network.EventLoadingFailed:
				requestMapMutex.Lock()
				requestURL := requestURLByID[typedEvent.RequestID]
				requestMapMutex.Unlock()
				chromeLogPrinter(chromeLogNetworkFailureMessage, requestURL, typedEvent.ErrorText, typedEvent.Canceled)
			case *target.EventTargetCrashed:
				chromeLogPrinter(chromeLogTargetCrashMessage)
			}
		})
	}

	var htmlContent string
	renderTasks := chromedp.Tasks{
		chromedp.ActionFunc(enableNetworkAndSetHeaders),
		chromedp.ActionFunc(disableAutomationDetection),
		chromedp.ActionFunc(applyStealthScripts),
	}
	if trimmedUserAgent != "" {
		ua := trimmedUserAgent
		renderTasks = append(renderTasks, chromedp.ActionFunc(func(c context.Context) error {
			return applyUserAgentOverride(c, ua)
		}))
	}
	if chromeLogPrinter != nil {
		chromeLogPrinter(chromeLogNavigationStartMessage, trimmedUserAgent, trimmedURL)
	}
	renderTasks = append(renderTasks,
		chromedp.Navigate(trimmedURL),
		// We poll readyState instead of relying on lifecycle events (avoids Page.setLifecycleEventsEnabled timing).
		chromedp.ActionFunc(waitForDocumentReadyStateComplete),
		chromedp.ActionFunc(func(c context.Context) error {
			return readDocumentOuterHTML(c, &htmlContent)
		}),
	)

	if err := chromedp.Run(chromeCtx, renderTasks...); err != nil {
		if chromeLogPrinter != nil {
			chromeLogPrinter(chromeLogNavigationErrorMessage, trimmedURL, err)
		}
		return "", err
	}
	if chromeLogPrinter != nil {
		chromeLogPrinter(chromeLogNavigationSuccessMessage, trimmedURL, len(htmlContent))
	}
	return htmlContent, nil
}

// Service resolves X/Twitter user IDs to handles (and display names).
type Service struct {
	cfg      Config
	rndMu    sync.Mutex
	rnd      *rand.Rand
	renderer Renderer
}

// NewService creates a resolver service using the given renderer (pass nil for default ChromeRenderer).
func NewService(cfg Config, renderer Renderer) *Service {
	seed := time.Now().UnixNano()
	if renderer == nil {
		renderer = NewChromeRenderer()
	}
	return &Service{
		cfg:      cfg,
		rnd:      rand.New(rand.NewSource(seed)),
		renderer: renderer,
	}
}

// ResolveBatch resolves all IDs in-order using a single network funnel with pacing.
func (s *Service) ResolveBatch(ctx context.Context, req Request) []Profile {
	results := make([]Profile, 0, len(req.IDs))
	processed := 0

	for _, id := range req.IDs {
		select {
		case <-ctx.Done():
			return results
		default:
		}

		perIDCtx := ctx
		var cancel context.CancelFunc
		if s.cfg.PerIDTimeout > 0 {
			perIDCtx, cancel = context.WithTimeout(ctx, s.cfg.PerIDTimeout)
		}
		started := time.Now()
		if s.cfg.Logf != nil {
			s.cfg.Logf("id=%s start (per-id timeout=%v)", id, s.cfg.PerIDTimeout)
		}

		pro := s.resolveWithRetries(perIDCtx, id)
		results = append(results, pro)

		if s.cfg.Logf != nil {
			s.cfg.Logf("id=%s done in %v err=%v", id, time.Since(started), condErr(pro.Err))
		}
		if cancel != nil {
			cancel()
		}

		processed++

		// pacing with jitter
		if sleep := s.jitterDuration(s.cfg.Delay, s.cfg.Jitter); sleep > 0 {
			if !s.sleepCtx(ctx, sleep) {
				return results
			}
		}
		// burst rest
		if s.cfg.BurstSize > 0 && processed%s.cfg.BurstSize == 0 {
			if rest := s.jitterDuration(s.cfg.BurstRest, s.cfg.BurstJitter); rest > 0 {
				if !s.sleepCtx(ctx, rest) {
					return results
				}
			}
		}
	}
	return results
}

func (s *Service) resolveWithRetries(ctx context.Context, id string) Profile {
	candidates := []string{
		"https://x.com/intent/user?user_id=" + id,
		"https://x.com/i/user/" + id,
	}

	attempts := s.cfg.Retries + 1
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		for _, url := range candidates {
			select {
			case <-ctx.Done():
				return Profile{ID: id, FromURL: url, Err: ctx.Err().Error()}
			default:
			}

			ua := s.pickUA()
			if s.cfg.Logf != nil {
				s.cfg.Logf("id=%s attempt=%d url=%s ua=%q", id, attempt+1, url, ua)
			}

			// Per-attempt timeout nests under per-ID timeout.
			attemptCtx := ctx
			var cancel context.CancelFunc
			if s.cfg.AttemptTimeout > 0 {
				attemptCtx, cancel = context.WithTimeout(ctx, s.cfg.AttemptTimeout)
			}
			renderCtx := attemptCtx
			if s.cfg.Logf != nil {
				renderCtx = withLogFunction(attemptCtx, s.cfg.Logf)
			}
			started := time.Now()
			htmlDoc, err := s.renderer.Render(renderCtx, ua, url, s.cfg.VirtualTimeBudgetMS, s.cfg.ChromePath)
			if cancel != nil {
				cancel()
			}
			if err != nil || strings.TrimSpace(htmlDoc) == "" {
				if s.cfg.Logf != nil {
					s.cfg.Logf("id=%s attempt=%d url=%s elapsed=%v err=%v empty=%v",
						id, attempt+1, url, time.Since(started), condErr(errStr(err)), strings.TrimSpace(htmlDoc) == "")
				}
				if err != nil {
					lastErr = err
				} else {
					lastErr = fmt.Errorf("empty document")
				}
				continue
			}

			normalized := strings.ReplaceAll(htmlDoc, `'`, `"`)
			handle := extractHandle(normalized)
			display := extractDisplayName(normalized, handle)
			if handle != "" {
				if s.cfg.Logf != nil {
					s.cfg.Logf("id=%s attempt=%d url=%s elapsed=%v OK handle=%s",
						id, attempt+1, url, time.Since(started), handle)
				}
				return Profile{
					ID:          id,
					Handle:      handle,
					DisplayName: display,
					FromURL:     url,
				}
			}
			lastErr = fmt.Errorf("no handle found")
		}

		// backoff before next attempt if we still have time
		if attempt < attempts-1 {
			sleep := s.backoffDuration(attempt)
			if sleep > 0 && !s.sleepCtx(ctx, sleep) {
				return Profile{ID: id, Err: ctx.Err().Error()}
			}
		}
	}
	msg := "unresolvable"
	if lastErr != nil {
		msg = lastErr.Error()
	}
	return Profile{ID: id, Err: msg}
}

func (s *Service) pickUA() string {
	if len(s.cfg.UserAgents) == 0 {
		return s.defaultChromeUserAgent()
	}
	idx := s.randIntn(len(s.cfg.UserAgents))
	return s.cfg.UserAgents[idx]
}

// DefaultChromeUserAgent returns a reasonable UA when none provided.
func DefaultChromeUserAgent(r *rand.Rand) string {
	if r == nil {
		r = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	return DefaultUAs[r.Intn(len(DefaultUAs))]
}

func (s *Service) jitterDuration(base, jitter time.Duration) time.Duration {
	if base < 0 {
		base = 0
	}
	if jitter <= 0 {
		return base
	}
	offset := (s.randFloat64()*2 - 1) * float64(jitter)
	d := time.Duration(float64(base) + offset)
	if d < 0 {
		return 0
	}
	return d
}

func (s *Service) sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// Simple backoff between retry attempts: grow from RetryMin toward RetryMax.
func (s *Service) backoffDuration(attempt int) time.Duration {
	min := s.cfg.RetryMin
	max := s.cfg.RetryMax
	if min <= 0 && max <= 0 {
		// sensible default
		min, max = 400*time.Millisecond, 1500*time.Millisecond
	}
	if min <= 0 {
		min = max / 2
	}
	if max < min {
		max = min
	}
	// linear-ish growth clipped to [min,max]
	scale := 1.0 + float64(attempt)
	d := time.Duration(float64(min) * scale)
	if d > max {
		d = max
	}
	// add small jitter (+/- 25%)
	j := time.Duration(0.25 * float64(d))
	offset := (s.randFloat64()*2 - 1) * float64(j)
	return time.Duration(float64(d) + offset)
}

func (s *Service) defaultChromeUserAgent() string {
	s.rndMu.Lock()
	defer s.rndMu.Unlock()
	return DefaultChromeUserAgent(s.rnd)
}

func (s *Service) randFloat64() float64 {
	s.rndMu.Lock()
	defer s.rndMu.Unlock()
	if s.rnd == nil {
		return rand.Float64()
	}
	return s.rnd.Float64()
}

func (s *Service) randIntn(n int) int {
	s.rndMu.Lock()
	defer s.rndMu.Unlock()
	if s.rnd == nil {
		return rand.Intn(n)
	}
	return s.rnd.Intn(n)
}

// ===== Helpers (pure) =====

func extractHandle(htmlDoc string) string {
	for _, full := range ProfileURLRegex.FindAllString(htmlDoc, -1) {
		h := stripDomainPrefix(full)
		if !isReserved(h) {
			return h
		}
	}
	return ""
}

func extractDisplayName(htmlDoc string, handle string) string {
	title := firstGroup(MetaOGTitle.FindStringSubmatch(htmlDoc))
	if title == "" {
		title = firstGroup(MetaTitleTag.FindStringSubmatch(htmlDoc))
	}
	if title == "" {
		return ""
	}
	if idx := strings.Index(title, "(@"); idx > 0 {
		return strings.TrimSpace(title[:idx])
	}
	name := strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(title, " / X"), " on X"))
	if handle != "" {
		name = strings.ReplaceAll(name, "(@"+handle+")", "")
		name = strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(name, " / X"), " on X"))
	}
	return strings.TrimSpace(name)
}

func stripDomainPrefix(fullURL string) string {
	fullURL = strings.TrimPrefix(fullURL, "https://")
	if i := strings.IndexByte(fullURL, '/'); i >= 0 {
		return fullURL[i+1:]
	}
	return fullURL
}

func isReserved(handle string) bool {
	_, bad := ReservedTopLevelPaths[strings.ToLower(handle)]
	return bad
}

func firstGroup(m []string) string {
	if len(m) >= 2 {
		return m[1]
	}
	return ""
}

func enableNetworkAndSetHeaders(chromedpCtx context.Context) error {
	if err := network.Enable().Do(chromedpCtx); err != nil {
		return err
	}
	headers := network.Headers{
		acceptLanguageHeaderName:          acceptLanguageHeaderValue,
		upgradeInsecureRequestsHeaderName: upgradeInsecureRequestsHeaderValue,
	}
	return network.SetExtraHTTPHeaders(headers).Do(chromedpCtx)
}

func disableAutomationDetection(chromedpCtx context.Context) error {
	return emulation.SetAutomationOverride(false).Do(chromedpCtx)
}

func applyStealthScripts(chromedpCtx context.Context) error {
	for _, script := range stealthScripts {
		if _, err := page.AddScriptToEvaluateOnNewDocument(script).Do(chromedpCtx); err != nil {
			return err
		}
	}
	return nil
}

func applyUserAgentOverride(chromedpCtx context.Context, userAgent string) error {
	ua := emulation.SetUserAgentOverride(userAgent).WithAcceptLanguage(acceptLanguageHeaderValue)
	if p := navigatorPlatformForUserAgent(userAgent); p != "" {
		ua = ua.WithPlatform(p)
	}
	if metadata := userAgentMetadataFromUserAgent(userAgent); metadata != nil {
		ua = ua.WithUserAgentMetadata(metadata)
	}
	return ua.Do(chromedpCtx)
}

func waitForDocumentReadyStateComplete(chromedpCtx context.Context) error {
	ticker := time.NewTicker(documentReadyStatePollInterval)
	defer ticker.Stop()

	var lastEvaluationError error
	for {
		var readyStateValue string
		evaluationErr := chromedp.Evaluate(documentReadyStateScript, &readyStateValue, chromedp.EvalAsValue).Do(chromedpCtx)
		if evaluationErr == nil {
			lastEvaluationError = nil
			if strings.EqualFold(strings.TrimSpace(readyStateValue), documentReadyStateCompleteValue) {
				return nil
			}
		} else {
			lastEvaluationError = evaluationErr
		}

		select {
		case <-chromedpCtx.Done():
			if lastEvaluationError != nil {
				return lastEvaluationError
			}
			return chromedpCtx.Err()
		case <-ticker.C:
		}
	}
}

func readDocumentOuterHTML(chromedpCtx context.Context, htmlContentDestination *string) error {
	if htmlContentDestination == nil {
		return fmt.Errorf(documentOuterHTMLNilDestinationError)
	}
	var documentOuterHTML string
	if err := chromedp.Evaluate(documentOuterHTMLScript, &documentOuterHTML, chromedp.EvalAsValue).Do(chromedpCtx); err != nil {
		return err
	}
	*htmlContentDestination = documentOuterHTML
	return nil
}

func navigatorPlatformForUserAgent(userAgent string) string {
	n := strings.ToLower(userAgent)
	switch {
	case strings.Contains(n, userAgentMacintoshToken):
		return navigatorPlatformMacValue
	case strings.Contains(n, userAgentWindowsToken):
		return navigatorPlatformWindowsValue
	case strings.Contains(n, userAgentLinuxToken):
		return navigatorPlatformLinuxValue
	default:
		return navigatorPlatformMacValue
	}
}

func userAgentMetadataFromUserAgent(userAgent string) *emulation.UserAgentMetadata {
	majorVersion, fullVersion := extractChromeVersions(userAgent)
	if majorVersion == "" || fullVersion == "" {
		return nil
	}
	pd := platformDetailsFromUserAgent(userAgent)
	return &emulation.UserAgentMetadata{
		Brands:          majorBrandVersions(majorVersion),
		FullVersionList: fullVersionBrandList(fullVersion),
		Platform:        pd.platform,
		PlatformVersion: pd.platformVersion,
		Architecture:    pd.architecture,
		Model:           pd.model,
		Mobile:          false,
		Bitness:         pd.bitness,
		Wow64:           pd.wow64,
	}
}

type userAgentPlatformDetails struct {
	platform        string
	platformVersion string
	architecture    string
	bitness         string
	model           string
	wow64           bool
}

func platformDetailsFromUserAgent(userAgent string) userAgentPlatformDetails {
	n := strings.ToLower(userAgent)
	d := userAgentPlatformDetails{
		platform:        userAgentPlatformMacOS,
		platformVersion: userAgentPlatformVersionDefault,
		architecture:    userAgentArchitectureX86,
		bitness:         userAgentBitness64,
		model:           "",
		wow64:           false,
	}
	switch {
	case strings.Contains(n, userAgentMacintoshToken):
		d.platform = userAgentPlatformMacOS
		d.platformVersion = macPlatformVersion(userAgent)
	case strings.Contains(n, userAgentWindowsToken):
		d.platform = userAgentPlatformWindows
		d.platformVersion = windowsPlatformVersion(userAgent)
		d.wow64 = strings.Contains(n, userAgentWow64Token)
	case strings.Contains(n, userAgentLinuxToken):
		d.platform = userAgentPlatformLinux
		d.platformVersion = userAgentPlatformVersionDefault
	default:
		d.platform = userAgentPlatformMacOS
		d.platformVersion = userAgentPlatformVersionDefault
	}
	return d
}

func extractChromeVersions(userAgent string) (string, string) {
	i := strings.Index(userAgent, userAgentChromeMarker)
	if i < 0 {
		return "", ""
	}
	section := userAgent[i+len(userAgentChromeMarker):]
	delim := indexOfVersionDelimiter(section)
	val := strings.TrimSpace(section[:delim])
	if val == "" {
		return "", ""
	}
	major := val
	if dot := strings.Index(val, "."); dot >= 0 {
		major = val[:dot]
	}
	return major, val
}

func macPlatformVersion(userAgent string) string {
	raw := parseVersionAfterToken(userAgent, userAgentMacVersionToken)
	if raw == "" {
		return userAgentPlatformVersionDefault
	}
	return canonicalizeVersion(strings.ReplaceAll(raw, userAgentTokenUnderscore, "."))
}

func windowsPlatformVersion(userAgent string) string {
	raw := parseVersionAfterToken(userAgent, userAgentWindowsVersionToken)
	if raw == "" {
		return userAgentPlatformVersionDefault
	}
	return canonicalizeVersion(raw)
}

func parseVersionAfterToken(userAgent, token string) string {
	i := strings.Index(userAgent, token)
	if i < 0 {
		return ""
	}
	section := userAgent[i+len(token):]
	delim := indexOfVersionDelimiter(section)
	return strings.TrimSpace(section[:delim])
}

func indexOfVersionDelimiter(section string) int {
	for i, ch := range section {
		if ch == versionDelimiterSpaceRune || ch == versionDelimiterSemicolonRune || ch == versionDelimiterParenRune {
			return i
		}
	}
	return len(section)
}

func canonicalizeVersion(v string) string {
	if v == "" {
		return userAgentPlatformVersionDefault
	}
	parts := strings.Split(v, ".")
	for len(parts) < 3 {
		parts = append(parts, "0")
	}
	if len(parts) > 3 {
		parts = parts[:3]
	}
	return strings.Join(parts, ".")
}

func majorBrandVersions(major string) []*emulation.UserAgentBrandVersion {
	if major == "" {
		return nil
	}
	return []*emulation.UserAgentBrandVersion{
		{Brand: chromeBrandNotABrandName, Version: chromeBrandNotABrandVersion},
		{Brand: chromeBrandChromiumName, Version: major},
		{Brand: chromeBrandGoogleChromeName, Version: major},
	}
}

func fullVersionBrandList(full string) []*emulation.UserAgentBrandVersion {
	if full == "" {
		return nil
	}
	return []*emulation.UserAgentBrandVersion{
		{Brand: chromeBrandChromiumName, Version: full},
		{Brand: chromeBrandGoogleChromeName, Version: full},
	}
}

func chromeProxyServerValue(targetURL string) string {
	u := strings.TrimSpace(targetURL)
	if u == "" {
		return ""
	}
	parsedURL, err := neturl.Parse(u)
	if err != nil || parsedURL.Scheme == "" || parsedURL.Host == "" {
		return ""
	}
	proxy := firstNonEmptyEnvValue(proxyEnvironmentKeys(parsedURL.Scheme)...)
	if proxy == "" && parsedURL.Scheme == "https" {
		proxy = firstNonEmptyEnvValue(proxyEnvironmentKeys("http")...)
	}
	if proxy == "" {
		proxy = firstNonEmptyEnvValue(allProxyEnvironmentUpper, allProxyEnvironmentLower)
	}
	if proxy == "" {
		return ""
	}
	if bypassProxy(parsedURL, firstNonEmptyEnvValue(noProxyEnvironmentUpper, noProxyEnvironmentLower)) {
		return ""
	}
	pp, perr := neturl.Parse(proxy)
	if perr != nil || strings.TrimSpace(pp.Host) == "" {
		return ""
	}
	return proxy
}

func proxyEnvironmentKeys(scheme string) []string {
	switch scheme {
	case "https":
		return []string{httpsProxyEnvironmentUpper, httpsProxyEnvironmentLower}
	case "http":
		return []string{httpProxyEnvironmentUpper, httpProxyEnvironmentLower}
	default:
		return nil
	}
}

func firstNonEmptyEnvValue(keys ...string) string {
	for _, k := range keys {
		if k == "" {
			continue
		}
		if v, ok := os.LookupEnv(k); ok {
			if s := strings.TrimSpace(v); s != "" {
				return s
			}
		}
	}
	return ""
}

func bypassProxy(targetURL *neturl.URL, noProxyList string) bool {
	if strings.TrimSpace(noProxyList) == "" {
		return false
	}
	host := strings.ToLower(targetURL.Hostname())
	port := targetURL.Port()
	if port == "" {
		port = defaultPortForScheme(targetURL.Scheme)
	}
	if host == "" {
		return false
	}

	for _, entry := range strings.Split(noProxyList, ",") {
		e := strings.TrimSpace(entry)
		if e == "" {
			continue
		}
		if e == "*" {
			return true
		}
		entryHost := e
		entryPort := ""
		if strings.Contains(e, ":") {
			if h, p, err := net.SplitHostPort(e); err == nil {
				entryHost, entryPort = h, p
			}
		}
		neh := strings.ToLower(strings.TrimPrefix(strings.TrimPrefix(entryHost, "*"), "."))
		if neh == "" {
			continue
		}
		if entryPort != "" && entryPort != port {
			continue
		}
		if host == neh || strings.HasSuffix(host, "."+neh) {
			return true
		}
	}
	return false
}

func defaultPortForScheme(s string) string {
	switch strings.ToLower(s) {
	case "https":
		return "443"
	case "http":
		return "80"
	default:
		return ""
	}
}

func condErr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func errStr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
