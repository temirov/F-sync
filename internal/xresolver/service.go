// internal/xresolver/service.go
package xresolver

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math/rand"
	"os/exec"
	"strings"
	"time"
)

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

func (r *ChromeRenderer) Render(ctx context.Context, userAgent, url string, vtBudgetMS int, chromePath string) (string, error) {
	if vtBudgetMS <= 0 {
		vtBudgetMS = 15000
	}
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
		url,
	}
	cmd := exec.CommandContext(ctx, chromePath, args...)
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

// Service resolves X/Twitter user IDs to handles (and display names).
type Service struct {
	cfg      Config
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
// It returns one Profile per input ID (same order).
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
			cancel() // immediate cancel per-id (don't defer across loop)
		}

		processed++

		// per-request pacing with jitter
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
			started := time.Now()
			htmlDoc, err := s.renderer.Render(attemptCtx, ua, url, s.cfg.VirtualTimeBudgetMS, s.cfg.ChromePath)
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
		return DefaultChromeUserAgent(s.rnd)
	}
	return s.cfg.UserAgents[s.rnd.Intn(len(s.cfg.UserAgents))]
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
	offset := (s.rnd.Float64()*2 - 1) * float64(jitter)
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
	// exponential-like growth clipped to [min,max]
	scale := 1.0 + float64(attempt)
	d := time.Duration(float64(min) * scale)
	if d > max {
		d = max
	}
	// add small jitter (+/- 25%)
	j := time.Duration(0.25 * float64(d))
	offset := (s.rnd.Float64()*2 - 1) * float64(j)
	return time.Duration(float64(d) + offset)
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
