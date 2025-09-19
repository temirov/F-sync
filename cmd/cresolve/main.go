// cmd/cresolve/main.go
package main

import (
	"bufio"
	"context"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/f-sync/fsync/internal/xresolver"
)

func main() {
	// Output modes
	flagCSV := flag.Bool("csv", false, "output CSV: id,handle,display_name")
	flagJSON := flag.Bool("json", false, "output JSON lines")

	// Resolver knobs
	flagChrome := flag.String("chrome", defaultChromePath(), "path to Chrome/Chromium binary (or set CHROME_BIN)")
	flagVT := flag.Int("vtbudget", 15000, "Chrome virtual time budget (ms)")
	flagTimeout := flag.Duration("timeout", 30*time.Second, "per-ID timeout")
	flagAttempt := flag.Duration("attempt-timeout", 15*time.Second, "per-attempt timeout (<= per-ID)")

	// Pacing knobs
	flagDelay := flag.Duration("delay", 500*time.Millisecond, "base delay between requests")
	flagJitter := flag.Duration("jitter", 0, "jitter applied to delay in [-jitter,+jitter]")
	flagBurstSize := flag.Int("burst-size", 0, "requests per burst (0 = no bursting)")
	flagBurstRest := flag.Duration("burst-rest", 0, "rest duration after each burst")
	flagBurstJitter := flag.Duration("burst-jitter", 0, "jitter for burst rest in [-burst-jitter,+burst-jitter]")

	// Retry knobs
	flagRetries := flag.Int("retries", 1, "additional attempts per ID (0 = single attempt)")
	flagRetryMin := flag.Duration("retry-min", 400*time.Millisecond, "minimum backoff between attempts")
	flagRetryMax := flag.Duration("retry-max", 1500*time.Millisecond, "maximum backoff between attempts")

	// UA rotation
	flagUAs := flag.String("ua-list", "", "comma-separated UA list to rotate (optional)")

	// Debug
	flagVerbose := flag.Bool("verbose", false, "verbose timings to stderr")

	flag.Parse()

	chromeBinaryPath := os.Getenv("CHROME_BIN")
	if chromeBinaryPath == "" {
		chromeBinaryPath = *flagChrome
	}

	ids := collectIDs(flag.Args())
	if len(ids) == 0 {
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			if s := strings.TrimSpace(sc.Text()); s != "" {
				ids = append(ids, s)
			}
		}
		if err := sc.Err(); err != nil {
			fmt.Fprintln(os.Stderr, "stdin read error:", err)
			os.Exit(2)
		}
	}
	if len(ids) == 0 {
		fmt.Fprintln(os.Stderr, "usage:")
		fmt.Fprintln(os.Stderr, "  cresolve <numeric_id>")
		fmt.Fprintln(os.Stderr, "  cresolve -csv <id1> <id2> ...")
		fmt.Fprintln(os.Stderr, "  cat ids.txt | cresolve -json")
		os.Exit(2)
	}

	cfg := xresolver.Config{
		ChromePath:          chromeBinaryPath,
		VirtualTimeBudgetMS: *flagVT,
		PerIDTimeout:        *flagTimeout,
		AttemptTimeout:      *flagAttempt,

		Delay:       *flagDelay,
		Jitter:      *flagJitter,
		BurstSize:   *flagBurstSize,
		BurstRest:   *flagBurstRest,
		BurstJitter: *flagBurstJitter,

		Retries:  *flagRetries,
		RetryMin: *flagRetryMin,
		RetryMax: *flagRetryMax,

		UserAgents: splitCSV(*flagUAs),
	}
	if *flagVerbose {
		cfg.Logf = func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, "[%s] ", time.Now().Format("15:04:05.000"))
			fmt.Fprintf(os.Stderr, format, args...)
			fmt.Fprintln(os.Stderr)
		}
	}

	// Construct service with a Chrome renderer.
	svc := xresolver.NewService(cfg, xresolver.NewChromeRenderer())

	// Cancel on SIGINT/SIGTERM
	rootCtx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Call the service
	res := svc.ResolveBatch(rootCtx, xresolver.Request{IDs: ids})

	// Presentation (CLI only)
	switch {
	case *flagJSON:
		enc := json.NewEncoder(os.Stdout)
		for _, p := range res {
			_ = enc.Encode(p)
		}
	case *flagCSV:
		w := csv.NewWriter(os.Stdout)
		_ = w.Write([]string{"id", "handle", "display_name"})
		for _, p := range res {
			_ = w.Write([]string{p.ID, p.Handle, p.DisplayName})
		}
		w.Flush()
	default:
		for _, p := range res {
			if p.Handle != "" {
				fmt.Printf("%s: %s (retrieved from %s)\n", p.ID, p.Handle, p.FromURL)
				if p.DisplayName != "" {
					fmt.Printf("  name: %s\n", p.DisplayName)
				}
			} else {
				fmt.Printf("%s:\n", p.ID)
				if p.Err != "" {
					fmt.Printf("  err:  %s\n", p.Err)
				}
			}
		}
	}
}

func collectIDs(args []string) []string {
	var out []string
	for _, a := range args {
		if s := strings.TrimSpace(a); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// defaultChromePath detects a sensible Chrome/Chromium path per-OS.
// Priority: CHROME_BIN (handled earlier), then common locations & PATH.
func defaultChromePath() string {
	// If CHROME_BIN is set, the caller will use it (handled in main).
	// Here we try platform defaults and PATH lookups.
	switch runtime.GOOS {
	case "darwin":
		// Standard macOS app bundle path
		return "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
	case "windows":
		// Try PATH first
		if p, err := exec.LookPath("chrome.exe"); err == nil {
			return p
		}
		// Common install paths (best-effort; user can override with --chrome)
		candidates := []string{
			`C:\Program Files\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
		}
		for _, c := range candidates {
			if _, err := os.Stat(c); err == nil {
				return c
			}
		}
		// Fallback: let it be empty; user must supply --chrome
		return "chrome.exe"
	default: // "linux" and others
		// Try common binary names in PATH
		names := []string{
			"google-chrome-stable",
			"google-chrome",
			"chromium",
			"chromium-browser",
			"chrome",
		}
		for _, n := range names {
			if p, err := exec.LookPath(n); err == nil {
				return p
			}
		}
		// Try a few absolute paths often used by packages/snap
		candidates := []string{
			"/usr/bin/google-chrome-stable",
			"/usr/bin/google-chrome",
			"/usr/bin/chromium",
			"/usr/bin/chromium-browser",
			"/snap/bin/chromium",
		}
		for _, c := range candidates {
			if _, err := os.Stat(c); err == nil {
				return c
			}
		}
		// Last resort: user must provide --chrome or CHROME_BIN
		return "google-chrome"
	}
}

func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
