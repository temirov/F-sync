// internal/xresolver/renderer_chromedp_remote.go
package xresolver

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/chromedp"
)

// RemoteChromedpRenderer uses a single long-lived Chrome started with --remote-debugging-port,
// then connects via chromedp.NewRemoteAllocator for each render.
type RemoteChromedpRenderer struct{}

func NewRemoteChromedpRenderer() *RemoteChromedpRenderer { return &RemoteChromedpRenderer{} }

// ---- singleton Chrome management ----

var (
	remoteOnce     sync.Once
	remoteInitErr  error
	remoteWSURL    string
	remoteCmd      *exec.Cmd
	remoteCmdGuard sync.Mutex
)

func (r *RemoteChromedpRenderer) Render(ctx context.Context, userAgent, url string, vtBudgetMS int, chromePath string) (string, error) {
	if strings.TrimSpace(url) == "" {
		return "", fmt.Errorf("empty url")
	}
	if vtBudgetMS <= 0 {
		vtBudgetMS = defaultVirtualTimeBudgetMilliseconds
	}

	// Ensure the background Chrome exists and we know its WS URL.
	if err := ensureRemoteChrome(ctx, chromePath, vtBudgetMS); err != nil {
		return "", err
	}

	// Build a remote allocator bound to the WS endpoint.
	allocCtx, cancelAlloc := chromedp.NewRemoteAllocator(ctx, remoteWSURL)
	defer cancelAlloc()

	// Optional logging passthrough
	chromeLogPrinter := logFunctionFromContext(ctx)
	ctxOpts := []chromedp.ContextOption{}
	if chromeLogPrinter != nil {
		ctxOpts = append(ctxOpts,
			chromedp.WithLogf(chromeLogPrinter),
			chromedp.WithErrorf(chromeLogPrinter),
			chromedp.WithDebugf(chromeLogPrinter),
		)
	}
	// Create a new tab/session
	chromeCtx, cancelChrome := chromedp.NewContext(allocCtx, ctxOpts...)
	defer cancelChrome()

	// Apply UA metadata (same helpers your other renderer uses)
	var tasks chromedp.Tasks
	tasks = append(tasks,
		chromedp.ActionFunc(enableNetworkAndSetHeaders),
		chromedp.ActionFunc(disableAutomationDetection),
		chromedp.ActionFunc(applyStealthScripts), // page.Enable() is called inside
	)

	if ua := strings.TrimSpace(userAgent); ua != "" {
		tasks = append(tasks, chromedp.ActionFunc(func(c context.Context) error {
			return applyUserAgentOverride(c, ua)
		}))
	}

	// Navigate and wait for readyState=complete (we avoid lifecycle events)
	var html string
	if chromeLogPrinter != nil {
		chromeLogPrinter(chromeLogNavigationStartMessage, userAgent, url)
	}
	tasks = append(tasks,
		chromedp.Navigate(url),
		chromedp.ActionFunc(waitForDocumentReadyStateComplete),
		chromedp.ActionFunc(func(c context.Context) error {
			return readDocumentOuterHTML(c, &html)
		}),
	)

	if err := chromedp.Run(chromeCtx, tasks...); err != nil {
		if chromeLogPrinter != nil {
			chromeLogPrinter(chromeLogNavigationErrorMessage, url, err)
		}
		return "", err
	}
	if chromeLogPrinter != nil {
		chromeLogPrinter(chromeLogNavigationSuccessMessage, url, len(html))
	}
	return html, nil
}

// Starts a single Chrome process on demand and captures its DevTools WS URL.
func ensureRemoteChrome(ctx context.Context, chromePath string, vtBudgetMS int) error {
	remoteOnce.Do(func() {
		if strings.TrimSpace(chromePath) == "" {
			// Also honor CHROME_BIN if wired elsewhere.
			chromePath = strings.TrimSpace(os.Getenv("CHROME_BIN"))
		}
		if strings.TrimSpace(chromePath) == "" {
			remoteInitErr = fmt.Errorf("missing chrome path")
			return
		}

		args := baseChromeArgs(vtBudgetMS)
		// Use a random free port by passing 0; Chrome prints the chosen WS URL
		args = append(args, "--remote-debugging-port=0")

		cmd := exec.Command(chromePath, args...)
		stderr, err := cmd.StderrPipe()
		if err != nil {
			remoteInitErr = err
			return
		}
		// stdout is unused; silence it
		if out, err := cmd.StdoutPipe(); err == nil {
			go io.Copy(io.Discard, out)
		}

		if err := cmd.Start(); err != nil {
			remoteInitErr = err
			return
		}
		remoteCmdGuard.Lock()
		remoteCmd = cmd
		remoteCmdGuard.Unlock()

		// Parse "DevTools listening on ws://127.0.0.1:XXXXX/devtools/browser/..."
		wsURL, parseErr := readDevToolsWSURL(stderr, 8*time.Second)
		if parseErr != nil {
			remoteInitErr = parseErr
			// Try to kill the process if we failed to parse
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
			return
		}
		remoteWSURL = wsURL

		// Reap Chrome if it dies unexpectedly
		go func() {
			_ = cmd.Wait()
			remoteCmdGuard.Lock()
			defer remoteCmdGuard.Unlock()
			remoteCmd = nil
		}()
	})

	return remoteInitErr
}

// Reads Chrome stderr until it finds the DevTools WS URL or times out.
func readDevToolsWSURL(r io.Reader, timeout time.Duration) (string, error) {
	type result struct {
		url string
		err error
	}
	ch := make(chan result, 1)

	go func() {
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			line := sc.Text()
			// Typical line: "DevTools listening on ws://127.0.0.1:55024/devtools/browser/..."
			if i := strings.Index(line, "DevTools listening on "); i >= 0 {
				ws := strings.TrimSpace(line[i+len("DevTools listening on "):])
				ch <- result{url: ws}
				return
			}
		}
		if err := sc.Err(); err != nil {
			ch <- result{err: err}
		} else {
			ch <- result{err: errors.New("chrome exited before DevTools URL was printed")}
		}
	}()

	select {
	case <-time.After(timeout):
		return "", errors.New("timed out waiting for DevTools WS URL")
	case res := <-ch:
		return res.url, res.err
	}
}

// Base flags we use to avoid renderer flakes; defaults are headless=new + ANGLE GPU.
// Env overrides:
//
//	XRESOLVER_HEADLESS_MODE=legacy|new
//	XRESOLVER_HEADLESS_GPU=0|1
func baseChromeArgs(vtBudgetMS int) []string {
	args := []string{
		// Default to modern headless with GPU via ANGLE.
		"--headless=new",
		"--disable-gpu=0",
		"--use-gl=angle",

		"--hide-scrollbars",
		"--no-first-run",
		"--no-default-browser-check",
		"--log-level=3",
		"--silent",
		"--disable-logging",
		"--remote-allow-origins=*",
		"--disable-features=AutomationControlled",
		"--disable-extensions",
		"--disable-component-extensions-with-background-pages",
		"--ignore-certificate-errors",
		"--proxy-bypass-list=*",
		"--disable-dev-shm-usage",
		"--window-size=1200,900",
		"--virtual-time-budget=" + strconv.Itoa(vtBudgetMS),
	}

	// Honor env overrides (kept consistent with service.go behavior).
	switch strings.ToLower(strings.TrimSpace(os.Getenv("XRESOLVER_HEADLESS_MODE"))) {
	case "legacy":
		// legacy boolean headless and SwiftShader
		args = replaceOrAppend(args, "--headless=new", "--headless")
		args = replaceOrAppend(args, "--disable-gpu=0", "--disable-gpu")
		args = replaceOrAppendKV(args, "--use-gl", "swiftshader")
	}

	switch os.Getenv("XRESOLVER_HEADLESS_GPU") {
	case "0":
		args = replaceOrAppend(args, "--disable-gpu=0", "--disable-gpu")
		args = replaceOrAppendKV(args, "--use-gl", "swiftshader")
	case "1":
		// keep ANGLE
	}

	// Linux sandbox flags only on Linux
	if runtime.GOOS == "linux" {
		args = append(args, "--no-sandbox", "--disable-setuid-sandbox")
	}
	return args
}

func replaceOrAppend(args []string, old, new string) []string {
	for i := range args {
		if args[i] == old {
			args[i] = new
			return args
		}
	}
	return append(args, new)
}

func replaceOrAppendKV(args []string, key, val string) []string {
	prefix := key + "="
	for i := range args {
		if strings.HasPrefix(args[i], prefix) {
			args[i] = prefix + val
			return args
		}
	}
	return append(args, prefix+val)
}
