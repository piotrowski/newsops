package feed

import (
	"context"
	"fmt"
	"io"
	"os"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-shiori/go-readability"
	"golang.org/x/sync/errgroup"

	"news-ops/internal/config"
)

// Result is the per-source outcome of a fetch.
type Result struct {
	Source      config.Source
	Items       []ParsedItem
	NotModified bool   // 304 response
	RawPath     string // "" when not modified or on error
	HTTPStatus  int
	Err         error  // per-source; never aborts the batch
}

// StateStore reads and writes feed_state table state.
type StateStore interface {
	Get(ctx context.Context, source string) (etag, lastModified string, err error)
	Put(ctx context.Context, source, feedURL, etag, lastModified string, status int, errMsg string) error
}

// Fetcher fetches feeds concurrently with per-host rate limiting and caching.
type Fetcher struct {
	Client    *http.Client
	UserAgent string
	Limit     int           // concurrent requests, default 10
	PerHost   time.Duration // min gap between requests to one host, default 1s
	CacheDir  string
}

// defaultUserAgent returns the standard User-Agent string.
func defaultUserAgent() string {
	return "newsops/dev (+personal feed reader)"
}

// Fetch returns one Result per source, in input order.
// The returned error is non-nil only for a whole-batch failure (context cancelled).
func (f *Fetcher) Fetch(ctx context.Context, srcs []config.Source, state StateStore) ([]Result, error) {
	if f.Client == nil {
		f.Client = &http.Client{Timeout: 20 * time.Second}
	}
	if f.UserAgent == "" {
		f.UserAgent = defaultUserAgent()
	}
	if f.Limit <= 0 {
		f.Limit = 10
	}
	if f.PerHost <= 0 {
		f.PerHost = time.Second
	}

	// Per-host rate limiting: mutex + last request timestamp.
	hostMu := new(sync.Mutex)
	hostLast := make(map[string]time.Time)

	results := make([]Result, len(srcs))

	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(f.Limit)

	for i, src := range srcs {
		i, src := i, src // capture loop vars
		g.Go(func() error {
			result := f.fetchOne(ctx, src, state, hostMu, hostLast)
			results[i] = result
			return nil // individual failures are in Result.Err
		})
	}

	if err := g.Wait(); err != nil {
		return results, err // errgroup-level failure
	}
	return results, nil
}

func (f *Fetcher) fetchOne(ctx context.Context, src config.Source, state StateStore, hostMu *sync.Mutex, hostLast map[string]time.Time) Result {
	r := Result{Source: src}

	// Per-host rate limiting.
	host := mustHost(src.URL)
	waitForRateLimit(hostMu, hostLast, host, f.PerHost)

	// Build request with conditional headers.
	reqCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, "GET", src.URL, nil)
	if err != nil {
		r.Err = fmt.Errorf("build request: %w", err)
		return f.finish(r, state)
	}
	req.Header.Set("User-Agent", f.UserAgent)

	// Conditional request headers.
	etag, lastModified, _ := state.Get(ctx, src.Name)
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	if lastModified != "" {
		req.Header.Set("If-Modified-Since", lastModified)
	}

	resp, err := f.Client.Do(req)
	if err != nil {
		r.Err = fmt.Errorf("fetch %s: %w", src.URL, err)
		return f.finish(r, state)
	}
	defer resp.Body.Close()

	r.HTTPStatus = resp.StatusCode

	// 304 Not Modified.
	if resp.StatusCode == http.StatusNotModified {
		r.NotModified = true
		_ = state.Put(ctx, src.Name, src.URL, etag, lastModified, resp.StatusCode, "")
		return r
	}

	// Read body.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20)) // 10 MB cap
	if err != nil {
		r.Err = fmt.Errorf("read %s: %w", src.URL, err)
		return f.finish(r, state)
	}

	// Parse feed.
	items, parseErr := Parse(body, src)
	if parseErr != nil {
		r.Err = fmt.Errorf("parse %s: %w", src.URL, parseErr)
		return f.finish(r, state)
	}
	r.Items = items

	// Cache write (best-effort).
	if f.CacheDir != "" && len(body) > 0 {
		path, err := Write(f.CacheDir, src.URL, body)
		if err != nil {
			fmt.Fprintf(os.Stderr, "newsops: cache write %s: %v\n", src.URL, err)
		} else {
			r.RawPath = path
		}
	}

	// Readability fallback for short-content tier 1/2 items.
	r.Items = f.readabilityFallback(ctx, r.Items, src)

	// Store ETag and Last-Modified from the response.
	respETag := resp.Header.Get("ETag")
	respLM := resp.Header.Get("Last-Modified")
	_ = state.Put(ctx, src.Name, src.URL, respETag, respLM, resp.StatusCode, "")
	return r
}

func (f *Fetcher) finish(r Result, state StateStore) Result {
	if r.Err != nil {
		_ = state.Put(context.Background(), r.Source.Name, r.Source.URL, "", "", 0, r.Err.Error())
	}
	return r
}

// readabilityFallback replaces Content with readability-extracted text for short items.
func (f *Fetcher) readabilityFallback(ctx context.Context, items []ParsedItem, src config.Source) []ParsedItem {
	if src.Tier > 2 {
		return items
	}

	// Count aggregator sources: skip if source host != article link host.
	srcHost := mustHost(src.URL)

	type fetchJob struct {
		idx    int
		link   string
		source string
	}

	var jobs []fetchJob
	for i, item := range items {
		if len(item.Content) >= 400 || item.Link == "" {
			continue
		}
		// Skip aggregators: link points to a different host.
		linkHost := mustHost(item.Link)
		if linkHost != "" && linkHost != srcHost {
			continue
		}
		jobs = append(jobs, fetchJob{i, item.Link, src.Name})
	}

	// Cap at 10 readability fetches per run.
	if len(jobs) > 10 {
		jobs = jobs[:10]
	}

	if len(jobs) == 0 {
		return items
	}

	g, ctx := errgroup.WithContext(ctx)
	results := make([]string, len(jobs)) // extracted text per job

	for j, job := range jobs {
		j, job := j, job
		g.Go(func() error {
			reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()

			req, err := http.NewRequestWithContext(reqCtx, "GET", job.link, nil)
			if err != nil {
				return err
			}
			req.Header.Set("User-Agent", f.UserAgent)

			resp, err := f.Client.Do(req)
			if err != nil {
				return err
			}
			defer resp.Body.Close()

			body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
			if err != nil {
				return err
			}

			doc, err := readability.FromReader(strings.NewReader(string(body)), nil)
			if err != nil {
				return err
			}
			results[j] = doc.TextContent
			return nil
		})
	}

	// Wait for all readability fetches; failures are silently ignored.
	g.Wait()

	for j, text := range results {
		if text == "" {
			continue
		}
		items[jobs[j].idx].Content = text
	}

	return items
}

// waitForRateLimit blocks until the minimum interval has elapsed since the last
// request to this host.
func waitForRateLimit(hostMu *sync.Mutex, hostLast map[string]time.Time, host string, perHost time.Duration) {
	hostMu.Lock()
	defer hostMu.Unlock()

	if last, ok := hostLast[host]; ok {
		elapsed := time.Since(last)
		if elapsed < perHost {
			time.Sleep(perHost - elapsed)
		}
	}
	hostLast[host] = time.Now()
}

// mustHost extracts the hostname from a URL, returning "" on parse failure.
func mustHost(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Host
}
