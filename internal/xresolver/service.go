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

	// Headless / GPU (modern)
	chromeHeadlessFlagKey      = "headless"
	chromeHeadlessModeNewValue = "new"

	// Noise reduction
	chromeDisableDevShmUsageFlagKey                 = "disable-dev-shm-usage"
	chromeDisableExtensionsFlagKey                  = "disable-extensions"
	chromeDisableComponentExtensionsBackground      = "disable-component-extensions-with-background-pages"
	chromeDisableBlinkFeaturesFlagKey               = "disable-blink-features"
	chromeAutomationControlledBlinkValue            = "AutomationControlled"
	chromeNoFirstRunFlagKey                         = "no-first-run"
	chromeNoDefaultBrowserCheckFlagKey              = "no-default-browser-check"
	chromeLogLevelFlagKey                           = "log-level"
	chromeSilentFlagKey                             = "silent"
	chromeDisableLoggingFlagKey                     = "disable-logging"
	chromeIgnoreCertificateErrorsFlag               = "ignore-certificate-errors"
	acceptLanguageHeaderName                        = "Accept-Language"
	acceptLanguageHeaderValue                       = "en-US,en;q=0.9"
	upgradeInsecureRequestsHeaderName               = "Upgrade-Insecure-Requests"
	upgradeInsecureRequestsHeaderValue              = "1"
	chromeUserAgentFlagKey                          = "user-agent"
	chromeVirtualTimeBudgetFlagKey                  = "virtual-time-budget"
	chromeProxyServerFlagKey                        = "proxy-server"
	chromeSilentLogLevelValue                       = "3"
	chromeRendererEmptyURLErrorMessage              = "empty url"
	chromeLogNavigationStartMessage                 = "chromedp navigate: user-agent=%q url=%s"
	chromeLogNavigationSuccessMessage               = "chromedp render success: url=%s bytes=%d"
	chromeLogNavigationErrorMessage                 = "chromedp render failure: url=%s err=%v"
	chromeLogNetworkRequestMessage                  = "chromedp network request: url=%s"
	chromeLogNetworkResponseMessage                 = "chromedp network response: url=%s status=%d"
	chromeLogNetworkFailureMessage                  = "chromedp network failure: url=%s error=%s canceled=%v"
	chromeLogTargetCrashMessage                     = "chromedp target crashed"
	documentReadyStateScript                        = "document.readyState"
	documentReadyStateCompleteValue                 = "complete"
	documentReadyStatePollInterval                  = 100 * time.Millisecond
	documentOuterHTMLScript                         = "document.documentElement.outerHTML"
	documentOuterHTMLNilDestinationError            = "html destination pointer is nil"
	navigatorPlatformMacValue                       = "MacIntel"
	navigatorPlatformWindowsValue                   = "Win32"
	navigatorPlatformLinuxValue                     = "Linux x86_64"
	navigatorWebdriverOverrideScript                = "Object.defineProperty(navigator, 'webdriver', { get: () => undefined });"
	navigatorLanguagesOverrideScript                = "Object.defineProperty(navigator, 'languages', { get: () => ['en-US','en'] });"
	navigatorPluginsOverrideScript                  = "Object.defineProperty(navigator, 'plugins', { get: () => [1, 2, 3, 4, 5] });"
	windowChromeRuntimeDefinitionScript             = "window.chrome = window.chrome || {}; window.chrome.runtime = {};"
	navigatorPermissionsOverrideScript              = "const originalQuery = window.navigator.permissions.query; window.navigator.permissions.query = (parameters) => (parameters && parameters.name === 'notifications' ? Promise.resolve({ state: 'default' }) : originalQuery(parameters));"
	userAgentChromeMarker                           = "Chrome/"
	userAgentMacintoshToken                         = "macintosh"
	userAgentWindowsToken                           = "windows"
	userAgentLinuxToken                             = "linux"
	userAgentMacVersionToken                        = "Mac OS X "
	userAgentWindowsVersionToken                    = "Windows NT "
	userAgentTokenUnderscore                        = "_"
	userAgentPlatformMacOS                          = "macOS"
	userAgentPlatformWindows                        = "Windows"
	userAgentPlatformLinux                          = "Linux"
	userAgentPlatformVersionDefault                 = "0.0.0"
	userAgentArchitectureX86                        = "x86"
	userAgentBitness64                              = "64"
	userAgentWow64Token                             = "wow64"
	versionDelimiterSpaceRune                  rune = ' '
	versionDelimiterSemicolonRune              rune = ';'
	versionDelimiterParenRune                  rune = ')'
	chromeBrandNotABrandName                        = "Not A(Brand"
	chromeBrandNotABrandVersion                     = "8"
	chromeBrandChromiumName                         = "Chromium"
	chromeBrandGoogleChromeName                     = "Google Chrome"

	// Proxy env
	httpsProxyEnvironmentUpper = "HTTPS_PROXY"
	httpsProxyEnvironmentLower = "https_proxy"
	httpProxyEnvironmentUpper  = "HTTP_PROXY"
	httpProxyEnvironmentLower  = "http_proxy"
	allProxyEnvironmentUpper   = "ALL_PROXY"
	allProxyEnvironmentLower   = "all_proxy"
	noProxyEnvironmentUpper    = "NO_PROXY"
	noProxyEnvironmentLower    = "no_proxy"
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
	ChromePath          string
	VirtualTimeBudgetMS int

	PerIDTimeout   time.Duration
	AttemptTimeout time.Duration

	// Request pacing (between IDs)
	Delay       time.Duration
	Jitter      time.Duration
	BurstSize   int
	BurstRest   time.Duration
	BurstJitter time.Duration

	// Retries (within same ID)
	Retries  int
	RetryMin time.Duration
	RetryMax time.Duration

	// UA rotation
	UserAgents []string

	// Optional debug logger; if nil, no logs.
	Logf func(format string, args ...any)
}

// Request is the payload for batch resolution.
type Request struct{ IDs []string }

// Profile is the result for a single ID.
type Profile struct {
	ID          string
	Handle      string
	DisplayName string
	FromURL     string
	Err         string
}

// Renderer abstracts how HTML is obtained.
type Renderer interface {
	Render(ctx context.Context, userAgent, url string, vtBudgetMS int, chromePath string) (string, error)
}

// ChromeRenderer uses a headless Chrome process (modern flags only).
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

	// Fast fail if Chrome path is set but invalid (keeps tests deterministic).
	if err := validateChromeBinary(chromePath); err != nil {
		return "", err
	}

	// --- Modern, 2025-friendly flags ---
	opts := []chromedp.ExecAllocatorOption{
		// Headless "new" is the default & non-deprecated mode.
		chromedp.Flag(chromeHeadlessFlagKey, chromeHeadlessModeNewValue),

		// Keep logs silent/no prompts; no GL/GPU legacy knobs.
		chromedp.Flag(chromeDisableDevShmUsageFlagKey, true),
		chromedp.Flag(chromeNoFirstRunFlagKey, true),
		chromedp.Flag(chromeNoDefaultBrowserCheckFlagKey, true),
		chromedp.Flag(chromeDisableExtensionsFlagKey, true),
		chromedp.Flag(chromeDisableComponentExtensionsBackground, true),
		chromedp.Flag(chromeDisableBlinkFeaturesFlagKey, chromeAutomationControlledBlinkValue),
		chromedp.Flag(chromeIgnoreCertificateErrorsFlag, true),
		chromedp.Flag(chromeLogLevelFlagKey, chromeSilentLogLevelValue),
		chromedp.Flag(chromeSilentFlagKey, true),
		chromedp.Flag(chromeDisableLoggingFlagKey, true),

		// Make sure CDP virtual time budget is honored.
		chromedp.Flag(chromeVirtualTimeBudgetFlagKey, strconv.Itoa(effectiveBudget)),
	}

	// Optional Linux sandbox relaxation (only if you ask for it).
	// Export CHROME_NO_SANDBOX=1 when running inside locked-down containers.
	if runtime.GOOS == "linux" && os.Getenv("CHROME_NO_SANDBOX") == "1" {
		opts = append(opts,
			chromedp.Flag("no-sandbox", true),
			chromedp.Flag("disable-setuid-sandbox", true),
		)
	}

	if proxy := chromeProxyServerValue(trimmedURL); proxy != "" {
		opts = append(opts, chromedp.Flag(chromeProxyServerFlagKey, proxy))
	}
	if trimmedUserAgent != "" {
		opts = append(opts, chromedp.Flag(chromeUserAgentFlagKey, trimmedUserAgent))
	}
	if p := strings.TrimSpace(chromePath); p != "" {
		opts = append(opts, chromedp.ExecPath(p))
	}

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, opts...)
	defer cancelAlloc()

	// Create an isolated browser context for this run.
	logf := logFunctionFromContext(ctx)
	copts := []chromedp.ContextOption{}
	if logf != nil {
		copts = append(copts, chromedp.WithLogf(logf), chromedp.WithErrorf(logf), chromedp.WithDebugf(logf))
	}
	chromeCtx, cancelChrome := chromedp.NewContext(allocCtx, copts...)
	defer cancelChrome()

	// Useful network/target logs (only if logger provided).
	if logf != nil {
		reqURLByID := map[network.RequestID]string{}
		var mu sync.Mutex
		chromedp.ListenTarget(chromeCtx, func(ev any) {
			switch e := ev.(type) {
			case *network.EventRequestWillBeSent:
				mu.Lock()
				reqURLByID[e.RequestID] = e.Request.URL
				mu.Unlock()
				logf(chromeLogNetworkRequestMessage, e.Request.URL)
			case *network.EventResponseReceived:
				mu.Lock()
				u := reqURLByID[e.RequestID]
				if u == "" {
					u = e.Response.URL
				}
				mu.Unlock()
				logf(chromeLogNetworkResponseMessage, u, int(e.Response.Status))
			case *network.EventLoadingFailed:
				mu.Lock()
				u := reqURLByID[e.RequestID]
				mu.Unlock()
				logf(chromeLogNetworkFailureMessage, u, e.ErrorText, e.Canceled)
			case *target.EventTargetCrashed:
				logf(chromeLogTargetCrashMessage)
			}
		})
	}

	var html string
	tasks := chromedp.Tasks{
		chromedp.ActionFunc(enableNetworkAndSetHeaders),
		chromedp.ActionFunc(disableAutomationDetection),
		chromedp.ActionFunc(applyStealthScripts),
	}
	if trimmedUserAgent != "" {
		ua := trimmedUserAgent
		tasks = append(tasks, chromedp.ActionFunc(func(c context.Context) error {
			return applyUserAgentOverride(c, ua)
		}))
	}
	if logf != nil {
		logf(chromeLogNavigationStartMessage, trimmedUserAgent, trimmedURL)
	}
	tasks = append(tasks,
		chromedp.Navigate(trimmedURL),
		chromedp.ActionFunc(waitForDocumentReadyStateComplete),
		chromedp.ActionFunc(func(c context.Context) error { return readDocumentOuterHTML(c, &html) }),
	)

	if err := chromedp.Run(chromeCtx, tasks...); err != nil {
		if logf != nil {
			logf(chromeLogNavigationErrorMessage, trimmedURL, err)
		}
		return "", err
	}
	if logf != nil {
		logf(chromeLogNavigationSuccessMessage, trimmedURL, len(html))
	}
	return html, nil
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

		if sleep := s.jitterDuration(s.cfg.Delay, s.cfg.Jitter); sleep > 0 {
			if !s.sleepCtx(ctx, sleep) {
				return results
			}
		}
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
				// Ensure the attempt time comfortably covers the virtual-time budget.
				min := time.Duration(s.cfg.VirtualTimeBudgetMS)*time.Millisecond + 3*time.Second
				at := s.cfg.AttemptTimeout
				if at < min {
					at = min
				}
				attemptCtx, cancel = context.WithTimeout(ctx, at)
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
				return Profile{ID: id, Handle: handle, DisplayName: display, FromURL: url}
			}
			lastErr = fmt.Errorf("no handle found")
		}

		if attempt < attempts-1 {
			if sleep := s.backoffDuration(attempt); sleep > 0 && !s.sleepCtx(ctx, sleep) {
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
	return s.cfg.UserAgents[s.randIntn(len(s.cfg.UserAgents))]
}

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

func (s *Service) backoffDuration(attempt int) time.Duration {
	min, max := s.cfg.RetryMin, s.cfg.RetryMax
	if min <= 0 && max <= 0 {
		min, max = 400*time.Millisecond, 1500*time.Millisecond
	}
	if min <= 0 {
		min = max / 2
	}
	if max < min {
		max = min
	}
	scale := 1.0 + float64(attempt)
	d := time.Duration(float64(min) * scale)
	if d > max {
		d = max
	}
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
	// Page must be enabled before injecting scripts for cross-version stability.
	if err := page.Enable().Do(chromedpCtx); err != nil {
		return err
	}
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
	t := time.NewTicker(documentReadyStatePollInterval)
	defer t.Stop()

	var lastErr error
	for {
		var s string
		err := chromedp.Evaluate(documentReadyStateScript, &s, chromedp.EvalAsValue).Do(chromedpCtx)
		if err == nil {
			lastErr = nil
			if strings.EqualFold(strings.TrimSpace(s), documentReadyStateCompleteValue) {
				return nil
			}
		} else {
			lastErr = err
		}
		select {
		case <-chromedpCtx.Done():
			if lastErr != nil {
				return lastErr
			}
			return chromedpCtx.Err()
		case <-t.C:
		}
	}
}

func readDocumentOuterHTML(chromedpCtx context.Context, dst *string) error {
	if dst == nil {
		return fmt.Errorf(documentOuterHTMLNilDestinationError)
	}
	var html string
	if err := chromedp.Evaluate(documentOuterHTMLScript, &html, chromedp.EvalAsValue).Do(chromedpCtx); err != nil {
		return err
	}
	*dst = html
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
	major, full := extractChromeVersions(userAgent)
	if major == "" || full == "" {
		return nil
	}
	pd := platformDetailsFromUserAgent(userAgent)
	return &emulation.UserAgentMetadata{
		Brands:          majorBrandVersions(major),
		FullVersionList: fullVersionBrandList(full),
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
	d := indexOfVersionDelimiter(section)
	val := strings.TrimSpace(section[:d])
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
	d := indexOfVersionDelimiter(section)
	return strings.TrimSpace(section[:d])
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
	parsed, err := neturl.Parse(u)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	proxy := firstNonEmptyEnvValue(proxyEnvironmentKeys(parsed.Scheme)...)
	if proxy == "" && parsed.Scheme == "https" {
		proxy = firstNonEmptyEnvValue(proxyEnvironmentKeys("http")...)
	}
	if proxy == "" {
		proxy = firstNonEmptyEnvValue(allProxyEnvironmentUpper, allProxyEnvironmentLower)
	}
	if proxy == "" {
		return ""
	}
	if bypassProxy(parsed, firstNonEmptyEnvValue(noProxyEnvironmentUpper, noProxyEnvironmentLower)) {
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

// --- local helpers ---

func validateChromeBinary(path string) error {
	p := strings.TrimSpace(path)
	if p == "" {
		return nil // let chromedp find Chrome on PATH
	}
	if _, err := os.Stat(p); err != nil {
		return fmt.Errorf("chrome binary not found at %q: %v", p, err)
	}
	return nil
}
