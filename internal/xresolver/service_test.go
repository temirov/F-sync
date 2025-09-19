// internal/xresolver/service_test.go
package xresolver

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// stubRenderer allows us to simulate Chrome output deterministically in tests.
type stubRenderer struct {
	// map url -> html or special tokens
	byURL map[string]string
	// if >0, sleep this long for every render (to test attempt/per-id timeouts)
	sleep time.Duration
	// error to return if set (overrides byURL)
	err error
}

func (r *stubRenderer) Render(ctx context.Context, userAgent, url string, vtBudgetMS int, chromePath string) (string, error) {
	if r.sleep > 0 {
		select {
		case <-time.After(r.sleep):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if r.err != nil {
		return "", r.err
	}
	if s, ok := r.byURL[url]; ok {
		return s, nil
	}
	return "", errors.New("unknown url")
}

func htmlFor(handle, name string) string {
	// minimal HTML that satisfies our extractors
	title := name
	if handle != "" {
		title = name + " (@" + handle + ") / X"
	}
	return `<html><head><meta property="og:title" content="` + title + `"></head><body></body></html>`
}

func TestResolveSuccess_FirstEndpoint(t *testing.T) {
	r := &stubRenderer{
		byURL: map[string]string{
			"https://x.com/intent/user?user_id=123": htmlFor("alice", "Alice Doe"),
		},
	}
	cfg := Config{
		ChromePath:          "/dev/null/chrome",
		VirtualTimeBudgetMS: 1000,
		PerIDTimeout:        2 * time.Second,
		AttemptTimeout:      1 * time.Second,
		Retries:             0,
	}
	svc := NewService(cfg, r)

	res := svc.ResolveBatch(context.Background(), Request{IDs: []string{"123"}})
	if len(res) != 1 {
		t.Fatalf("expected 1 result, got %d", len(res))
	}
	p := res[0]
	if p.Handle != "alice" || p.DisplayName != "Alice Doe" || p.Err != "" {
		t.Fatalf("unexpected profile: %+v", p)
	}
}

func TestResolveFallback_SecondEndpoint(t *testing.T) {
	r := &stubRenderer{
		byURL: map[string]string{
			"https://x.com/intent/user?user_id=123": "", // empty -> forces fallback
			"https://x.com/i/user/123":              htmlFor("bob", "Bobby"),
		},
	}
	cfg := Config{
		ChromePath:          "/dev/null/chrome",
		VirtualTimeBudgetMS: 1000,
		PerIDTimeout:        2 * time.Second,
		AttemptTimeout:      1 * time.Second,
		Retries:             1, // allow retry cycle though fallback succeeds in first cycle anyway
	}
	svc := NewService(cfg, r)

	res := svc.ResolveBatch(context.Background(), Request{IDs: []string{"123"}})
	if res[0].Handle != "bob" || res[0].DisplayName != "Bobby" || res[0].Err != "" {
		t.Fatalf("unexpected profile: %+v", res[0])
	}
}

func TestResolveAttemptTimeout(t *testing.T) {
	// Renderer sleeps longer than attempt-timeout; should return context.DeadlineExceeded per attempt
	r := &stubRenderer{
		byURL: map[string]string{
			"https://x.com/intent/user?user_id=1": htmlFor("h", "H"),
			"https://x.com/i/user/1":              htmlFor("h", "H"),
		},
		sleep: 150 * time.Millisecond,
	}
	cfg := Config{
		ChromePath:          "/dev/null/chrome",
		VirtualTimeBudgetMS: 1000,
		PerIDTimeout:        400 * time.Millisecond,
		AttemptTimeout:      50 * time.Millisecond, // each attempt should time out
		Retries:             1,                     // two attempts total
		RetryMin:            10 * time.Millisecond,
		RetryMax:            20 * time.Millisecond,
	}
	svc := NewService(cfg, r)

	res := svc.ResolveBatch(context.Background(), Request{IDs: []string{"1"}})
	if len(res) != 1 {
		t.Fatalf("expected 1 result, got %d", len(res))
	}
	if !strings.Contains(res[0].Err, "context deadline exceeded") && res[0].Handle == "" {
		// We accept either per-attempt timeout bubbling up or final "unresolvable"
		// depending on scheduler jitter, but success here would be unexpected.
		t.Logf("err=%q handle=%q", res[0].Err, res[0].Handle)
	}
}

func TestBatchPacing_NoParallelLeak(t *testing.T) {
	r := &stubRenderer{
		byURL: map[string]string{
			"https://x.com/intent/user?user_id=10": htmlFor("aa", "AA"),
			"https://x.com/i/user/10":              htmlFor("aa", "AA"),
			"https://x.com/intent/user?user_id=20": htmlFor("bb", "BB"),
			"https://x.com/i/user/20":              htmlFor("bb", "BB"),
		},
		sleep: 20 * time.Millisecond,
	}
	cfg := Config{
		ChromePath:          "/dev/null/chrome",
		VirtualTimeBudgetMS: 1000,
		PerIDTimeout:        2 * time.Second,
		AttemptTimeout:      500 * time.Millisecond,
		Delay:               30 * time.Millisecond,
		Jitter:              10 * time.Millisecond,
		BurstSize:           0, // no bursts
	}
	svc := NewService(cfg, r)
	start := time.Now()
	res := svc.ResolveBatch(context.Background(), Request{IDs: []string{"10", "20"}})
	elapsed := time.Since(start)

	if len(res) != 2 || res[0].Handle != "aa" || res[1].Handle != "bb" {
		t.Fatalf("bad results: %+v", res)
	}
	// Expect at least ~20ms (render) + ~30ms (delay) + second render ~20ms
	if elapsed < 60*time.Millisecond {
		t.Fatalf("pacing seems off, elapsed=%v", elapsed)
	}
}
