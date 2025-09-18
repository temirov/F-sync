// cmd/cresolve/main.go
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"
)

const (
	defaultChromeBinaryPath = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
)

var (
	profileURLRegex = regexp.MustCompile(`https://(?:x|twitter)\.com/[A-Za-z0-9_]{1,15}`)

	reservedTopLevelPaths = map[string]struct{}{
		"i": {}, "intent": {}, "home": {}, "tos": {}, "privacy": {}, "explore": {},
		"notifications": {}, "settings": {}, "login": {}, "signup": {}, "share": {},
		"account": {}, "compose": {}, "messages": {}, "search": {},
	}

	metaOGTitle  = regexp.MustCompile(`property="og:title"[^>]*content="([^"]+)"`)
	metaTitleTag = regexp.MustCompile(`<title[^>]*>([^<]+)</title>`)

	uaPool = []string{
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 14_5_0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.846.0 Safari/537.36",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.846.0 Safari/537.36",
		"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.846.0 Safari/537.36",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 13_6_1) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.850.0 Safari/537.36",
	}
)

type profileInfo struct {
	ID          string `json:"id"`
	Handle      string `json:"handle"`
	DisplayName string `json:"display_name"`
	FromURL     string `json:"from_url"`
	Err         string `json:"error,omitempty"`
}

func main() {
	flagCSV := flag.Bool("csv", false, "output CSV: id,handle,display_name")
	flagJSON := flag.Bool("json", false, "output JSON lines")
	flagChrome := flag.String("chrome", defaultChromePath(), "path to Chrome/Chromium binary (or set CHROME_BIN)")
	flagVT := flag.Int("vtbudget", 15000, "Chrome virtual time budget (ms)")
	flagTimeout := flag.Duration("timeout", 30*time.Second, "per-ID timeout")
	flagDelay := flag.Duration("delay", 500*time.Millisecond, "fixed delay between requests")
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

	rand.Seed(time.Now().UnixNano())

	rootCtx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// CSV writer if needed — write header ONCE
	var csvWriter *csv.Writer
	if *flagCSV {
		csvWriter = csv.NewWriter(os.Stdout)
		_ = csvWriter.Write([]string{"id", "handle", "display_name"})
		csvWriter.Flush()
	}

	for _, id := range ids {
		select {
		case <-rootCtx.Done():
			return
		default:
		}

		perIDCtx, perCancel := context.WithTimeout(rootCtx, *flagTimeout)
		info := resolveOne(perIDCtx, chromeBinaryPath, *flagVT, id)
		perCancel()

		switch {
		case *flagJSON:
			enc := json.NewEncoder(os.Stdout)
			_ = enc.Encode(info)
		case *flagCSV:
			_ = csvWriter.Write([]string{info.ID, info.Handle, info.DisplayName})
			csvWriter.Flush()
		default:
			if info.Handle != "" {
				fmt.Printf("%s: %s (retrieved from %s)\n", info.ID, info.Handle, info.FromURL)
				if info.DisplayName != "" {
					fmt.Printf("  name: %s\n", info.DisplayName)
				}
			} else {
				fmt.Printf("%s:\n", info.ID)
				if info.Err != "" {
					fmt.Printf("  err:  %s\n", info.Err)
				}
			}
		}

		select {
		case <-time.After(*flagDelay):
		case <-rootCtx.Done():
			return
		}
	}
}

func resolveOne(ctx context.Context, chromeBinaryPath string, vtBudgetMS int, id string) profileInfo {
	intentURL := "https://x.com/intent/user?user_id=" + id
	userAgent := uaPool[rand.Intn(len(uaPool))]

	htmlDoc, err := renderWithHeadlessChrome(ctx, chromeBinaryPath, userAgent, vtBudgetMS, intentURL)
	if err != nil || strings.TrimSpace(htmlDoc) == "" {
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		return profileInfo{ID: id, FromURL: intentURL, Err: msg}
	}

	normalized := strings.ReplaceAll(htmlDoc, `'`, `"`)
	handle := extractHandle(normalized)
	display := extractDisplayName(normalized, handle)

	return profileInfo{
		ID:          id,
		Handle:      handle,
		DisplayName: display,
		FromURL:     intentURL,
	}
}

func renderWithHeadlessChrome(ctx context.Context, chromeBinaryPath, userAgent string, vtBudgetMS int, targetPageURL string) (string, error) {
	args := []string{
		"--headless=new",
		"--disable-gpu",
		"--use-gl=swiftshader",
		"--enable-unsafe-swiftshader",
		"--hide-scrollbars",
		"--no-first-run",
		"--no-default-browser-check",
		"--log-level=3",
		"--silent",
		"--disable-logging",
		"--user-agent=" + userAgent,
		fmt.Sprintf("--virtual-time-budget=%d", vtBudgetMS),
		"--dump-dom",
		targetPageURL,
	}
	cmd := exec.CommandContext(ctx, chromeBinaryPath, args...)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		return "", err
	}

	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	select {
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		<-waitCh
		return "", ctx.Err()
	case err := <-waitCh:
		if err != nil {
			return "", err
		}
		return stdout.String(), nil
	}
}

func extractHandle(htmlDoc string) string {
	for _, full := range profileURLRegex.FindAllString(htmlDoc, -1) {
		h := stripDomainPrefix(full)
		if !isReserved(h) {
			return h
		}
	}
	return ""
}

func extractDisplayName(htmlDoc string, handle string) string {
	title := firstGroup(metaOGTitle.FindStringSubmatch(htmlDoc))
	if title == "" {
		title = firstGroup(metaTitleTag.FindStringSubmatch(htmlDoc))
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
	_, bad := reservedTopLevelPaths[strings.ToLower(handle)]
	return bad
}

func firstGroup(m []string) string {
	if len(m) >= 2 {
		return m[1]
	}
	return ""
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

func defaultChromePath() string {
	if v := os.Getenv("CHROME_BIN"); v != "" {
		return v
	}
	return defaultChromeBinaryPath
}
