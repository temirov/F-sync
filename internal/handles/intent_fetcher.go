package handles

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const (
	chromeBinaryEnvironmentVariable      = "CHROME_BIN"
	defaultChromeUserAgent               = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/118.0.0.0 Safari/537.36"
	chromeUserAgentFlagFormat            = "--user-agent=%s"
	chromeVirtualTimeBudgetFlagFormat    = "--virtual-time-budget=%d"
	chromeDumpDOMFlag                    = "--dump-dom"
	chromeHeadlessFlag                   = "--headless=new"
	chromeDisableGPUFlag                 = "--disable-gpu"
	chromeUseGLSwiftShaderFlag           = "--use-gl=swiftshader"
	chromeEnableUnsafeSwiftShaderFlag    = "--enable-unsafe-swiftshader"
	chromeHideScrollbarsFlag             = "--hide-scrollbars"
	chromeNoFirstRunFlag                 = "--no-first-run"
	chromeNoDefaultBrowserCheckFlag      = "--no-default-browser-check"
	chromeLogLevelFlag                   = "--log-level=3"
	chromeSilentFlag                     = "--silent"
	chromeDisableLoggingFlag             = "--disable-logging"
	chromeDisableGPUStartupFlag          = "--disable-gpu-startup"
	chromeVirtualTimeBudgetDefaultMillis = 15000
	chromeRequestDelayDefaultMillis      = 500
	chromeBinaryPathMacOS                = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
	chromeBinaryPathLinux                = "/usr/bin/google-chrome"
	chromeBinaryNameLinux                = "google-chrome"
	chromeBinaryPathChromium             = "/usr/bin/chromium"
	chromeBinaryNameChromium             = "chromium"
	chromeBinaryFallback                 = chromeBinaryNameLinux
	errMessageMissingChromeBinaryPath    = "chrome binary path could not be determined"

	// ChromeBinaryEnvironmentVariable exposes the environment variable name used to
	// locate a Chrome binary for intent resolution.
	ChromeBinaryEnvironmentVariable = chromeBinaryEnvironmentVariable
)

var (
	baseChromeArguments = []string{
		chromeHeadlessFlag,
		chromeDisableGPUFlag,
		chromeDisableGPUStartupFlag,
		chromeUseGLSwiftShaderFlag,
		chromeEnableUnsafeSwiftShaderFlag,
		chromeHideScrollbarsFlag,
		chromeNoFirstRunFlag,
		chromeNoDefaultBrowserCheckFlag,
		chromeLogLevelFlag,
		chromeSilentFlag,
		chromeDisableLoggingFlag,
	}

	defaultChromeBinaryCandidates = []string{
		chromeBinaryPathMacOS,
		chromeBinaryPathLinux,
		chromeBinaryNameLinux,
		chromeBinaryPathChromium,
		chromeBinaryNameChromium,
	}
)

// IntentRequest describes the request to fetch an intent page for a numeric account identifier.
type IntentRequest struct {
	AccountID string
	URL       string
}

// IntentPage captures the rendered HTML for a Twitter intent page.
type IntentPage struct {
	HTML      string
	SourceURL string
}

// IntentFetcher retrieves rendered intent pages.
type IntentFetcher interface {
	FetchIntentPage(ctx context.Context, request IntentRequest) (IntentPage, error)
}

// ChromeFetcherConfig configures a ChromeIntentFetcher instance.
type ChromeFetcherConfig struct {
	BinaryPath        string
	UserAgent         string
	VirtualTimeBudget time.Duration
	RequestDelay      time.Duration
}

// ChromeIntentFetcher renders intent pages using a headless Chrome invocation.
type ChromeIntentFetcher struct {
	chromeBinaryPath  string
	userAgent         string
	virtualTimeBudget time.Duration
	requestDelay      time.Duration

	executionMutex sync.Mutex
	lastInvocation time.Time
}

// NewChromeIntentFetcher constructs a ChromeIntentFetcher from configuration values.
func NewChromeIntentFetcher(configuration ChromeFetcherConfig) (*ChromeIntentFetcher, error) {
	trimmedBinaryPath := strings.TrimSpace(configuration.BinaryPath)
	if trimmedBinaryPath == "" {
		return nil, fmt.Errorf(errMessageMissingChromeBinaryPath)
	}

	userAgent := strings.TrimSpace(configuration.UserAgent)
	if userAgent == "" {
		userAgent = defaultChromeUserAgent
	}

	virtualTimeBudget := configuration.VirtualTimeBudget
	if virtualTimeBudget <= 0 {
		virtualTimeBudget = time.Duration(chromeVirtualTimeBudgetDefaultMillis) * time.Millisecond
	}

	requestDelay := configuration.RequestDelay
	if requestDelay < 0 {
		requestDelay = 0
	} else if requestDelay == 0 {
		requestDelay = time.Duration(chromeRequestDelayDefaultMillis) * time.Millisecond
	}

	fetcher := &ChromeIntentFetcher{
		chromeBinaryPath:  trimmedBinaryPath,
		userAgent:         userAgent,
		virtualTimeBudget: virtualTimeBudget,
		requestDelay:      requestDelay,
	}
	return fetcher, nil
}

// FetchIntentPage renders the provided intent URL using headless Chrome.
func (fetcher *ChromeIntentFetcher) FetchIntentPage(ctx context.Context, request IntentRequest) (IntentPage, error) {
	fetcher.executionMutex.Lock()
	defer fetcher.executionMutex.Unlock()

	fetcher.enforceDelay()
	defer fetcher.recordInvocation()

	htmlContent, renderErr := fetcher.renderIntentPage(ctx, request.URL)
	if renderErr != nil {
		return IntentPage{}, renderErr
	}
	if strings.TrimSpace(htmlContent) == "" {
		return IntentPage{}, fmt.Errorf("%w: %s", errEmptyIntentHTML, request.URL)
	}

	return IntentPage{HTML: htmlContent, SourceURL: request.URL}, nil
}

func (fetcher *ChromeIntentFetcher) enforceDelay() {
	if fetcher.requestDelay <= 0 {
		return
	}
	if fetcher.lastInvocation.IsZero() {
		return
	}
	elapsed := time.Since(fetcher.lastInvocation)
	if elapsed < fetcher.requestDelay {
		time.Sleep(fetcher.requestDelay - elapsed)
	}
}

func (fetcher *ChromeIntentFetcher) recordInvocation() {
	fetcher.lastInvocation = time.Now()
}

func (fetcher *ChromeIntentFetcher) renderIntentPage(ctx context.Context, requestURL string) (string, error) {
	commandArguments := fetcher.buildCommandArguments(requestURL)
	command := exec.CommandContext(ctx, fetcher.chromeBinaryPath, commandArguments...)
	var stdout bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = io.Discard

	if startErr := command.Start(); startErr != nil {
		return "", startErr
	}

	waitChannel := make(chan error, 1)
	go func() {
		waitChannel <- command.Wait()
	}()

	select {
	case <-ctx.Done():
		_ = command.Process.Kill()
		<-waitChannel
		return "", ctx.Err()
	case waitErr := <-waitChannel:
		if waitErr != nil {
			return "", waitErr
		}
		return stdout.String(), nil
	}
}

func (fetcher *ChromeIntentFetcher) buildCommandArguments(requestURL string) []string {
	arguments := append([]string{}, baseChromeArguments...)
	userAgentArgument := fmt.Sprintf(chromeUserAgentFlagFormat, fetcher.userAgent)
	arguments = append(arguments, userAgentArgument)
	if fetcher.virtualTimeBudget > 0 {
		budgetMillis := int(fetcher.virtualTimeBudget / time.Millisecond)
		arguments = append(arguments, fmt.Sprintf(chromeVirtualTimeBudgetFlagFormat, budgetMillis))
	}
	arguments = append(arguments, chromeDumpDOMFlag, requestURL)
	return arguments
}

func resolveChromeBinaryPath(configuration Config) string {
	if trimmed := strings.TrimSpace(configuration.ChromeBinaryPath); trimmed != "" {
		return trimmed
	}
	if environmentValue := strings.TrimSpace(os.Getenv(ChromeBinaryEnvironmentVariable)); environmentValue != "" {
		return environmentValue
	}
	for _, candidate := range defaultChromeBinaryCandidates {
		if resolvedPath, lookErr := exec.LookPath(candidate); lookErr == nil {
			return resolvedPath
		}
	}
	return chromeBinaryFallback
}

// ResolveChromeBinaryPath determines the Chrome binary path for the supplied configuration.
func ResolveChromeBinaryPath(configuration Config) string {
	return resolveChromeBinaryPath(configuration)
}
