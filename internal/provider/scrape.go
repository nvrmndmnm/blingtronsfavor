package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Scrape hygiene shared by the scrape providers — the Wago data-page scrape
// and, later, the CurseForge cfwidget scrape (ISSUE-14). Neither service
// publishes a rate limit, so bfctl paces itself conservatively: at most one
// request per scrapeInterval process-wide (sync resolves addons concurrently,
// so the pacing gate is shared by every provider), a descriptive User-Agent
// that identifies bfctl and its repository, and exponential backoff on the
// transient failure shapes (429 and 5xx) a scraper must expect.
var (
	// scrapeUserAgent identifies bfctl to the scraped services.
	scrapeUserAgent = "bfctl/1 (WoW addon manager; +https://github.com/itp5b5/blessforge)"

	// scrapeInterval is the minimum gap between two scrape requests.
	// Tests lower it so fixture runs do not sleep.
	scrapeInterval = time.Second

	// scrapeBackoffBase is the first backoff wait before a retried request;
	// it doubles per attempt. Tests lower it.
	scrapeBackoffBase = time.Second

	// scrapeAttempts bounds one scrape request including retries.
	scrapeAttempts = 3

	// scrapeGate serializes the pacing of scrape requests across the
	// process: a request may not start before scrapeNext.
	scrapeGate struct {
		mu   sync.Mutex
		next time.Time
	}
)

// scrapeWait blocks until this goroutine's turn under the pacing gate.
func scrapeWait(ctx context.Context) error {
	scrapeGate.mu.Lock()
	now := time.Now()
	start := scrapeGate.next
	if start.Before(now) {
		start = now
	}
	scrapeGate.next = start.Add(scrapeInterval)
	scrapeGate.mu.Unlock()

	wait := time.Until(start)
	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer func() { _ = timer.Stop() }()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// scrapeGet performs one paced GET with retries on 429/5xx responses. A
// successful response is returned open — the caller closes the body. Retry
// waits follow exponential backoff and honor a Retry-After header when one
// is present.
func scrapeGet(ctx context.Context, httpClient *http.Client, url string) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt < scrapeAttempts; attempt++ {
		if attempt > 0 {
			if err := scrapeSleep(ctx, scrapeBackoff(attempt, lastErr)); err != nil {
				return nil, err
			}
		}
		if err := scrapeWait(ctx); err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, fmt.Errorf("build request for %s: %w", url, err)
		}
		req.Header.Set("User-Agent", scrapeUserAgent)
		req.Header.Set("Accept", "text/html, application/json")
		resp, err := httpClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			retryAfter := retryAfter(resp)
			_ = resp.Body.Close()
			lastErr = fmt.Errorf("%s: %s", url, resp.Status)
			if retryAfter > 0 {
				lastErr = &scrapeRetryError{after: retryAfter, err: lastErr}
			}
			continue
		}
		return resp, nil
	}
	if lastErr == nil {
		lastErr = errors.New("no request attempted")
	}
	return nil, fmt.Errorf("GET %s failed after %d attempts: %w", url, scrapeAttempts, lastErr)
}

// scrapeRetryError carries a server-provided Retry-After wait.
type scrapeRetryError struct {
	after time.Duration
	err   error
}

func (e *scrapeRetryError) Error() string { return e.err.Error() }
func (e *scrapeRetryError) Unwrap() error { return e.err }

// scrapeBackoff computes the wait before retry attempt (1-based), honoring a
// Retry-After hint when the last error carried one.
func scrapeBackoff(attempt int, lastErr error) time.Duration {
	var re *scrapeRetryError
	if errors.As(lastErr, &re) && re.after > 0 {
		return re.after
	}
	return scrapeBackoffBase << (attempt - 1)
}

// scrapeSleep waits for d or until ctx is done.
func scrapeSleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer func() { _ = timer.Stop() }()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// retryAfter parses a Retry-After header in seconds, if any.
func retryAfter(resp *http.Response) time.Duration {
	raw := resp.Header.Get("Retry-After")
	if raw == "" {
		return 0
	}
	secs, err := strconv.Atoi(raw)
	if err != nil || secs <= 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
}
