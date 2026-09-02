package feed_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"news-ops/internal/config"
	"news-ops/internal/feed"
)

// mockStateStore implements feed.StateStore for testing.
type mockStateStore struct {
	mu   sync.Mutex
	data map[string]stateEntry
}

type stateEntry struct {
	etag, lastModified string
}

func (m *mockStateStore) Get(ctx context.Context, source string) (string, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.data[source]
	if !ok {
		return "", "", nil
	}
	return e.etag, e.lastModified, nil
}

func (m *mockStateStore) Put(ctx context.Context, source, feedURL, etag, lastModified string, status int, errMsg string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.data == nil {
		m.data = make(map[string]stateEntry)
	}
	m.data[source] = stateEntry{etag: etag, lastModified: lastModified}
	return nil
}

func TestFetcher_concurrencyCeiling(t *testing.T) {
	var mu sync.Mutex
	var inFlight int
	var maxInFlight int

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		inFlight++
		if inFlight > maxInFlight {
			maxInFlight = inFlight
		}
		mu.Unlock()

		time.Sleep(50 * time.Millisecond) // simulate work

		mu.Lock()
		inFlight--
		mu.Unlock()

		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write([]byte(rssFixture))
	}))
	defer ts.Close()

	sources := make([]config.Source, 15)
	for i := range sources {
		sources[i] = config.Source{Name: "src", URL: ts.URL}
	}

	store := &mockStateStore{}
	f := &feed.Fetcher{
		Client:  http.DefaultClient,
		Limit:   10,
		PerHost: 0, // same host, but concurrency test overrides this
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	results, err := f.Fetch(ctx, sources, store)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(results) != 15 {
		t.Fatalf("expected 15 results, got %d", len(results))
	}

	if maxInFlight > 10 {
		t.Errorf("max in-flight was %d, expected ≤10", maxInFlight)
	}
}

func TestFetcher_perHostSpacing(t *testing.T) {
	var mu sync.Mutex
	requestTimes := make([]time.Time, 0)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requestTimes = append(requestTimes, time.Now())
		mu.Unlock()

		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write([]byte(rssFixture))
	}))
	defer ts.Close()

	sources := make([]config.Source, 5)
	for i := range sources {
		sources[i] = config.Source{Name: "src", URL: ts.URL}
	}

	store := &mockStateStore{}
	f := &feed.Fetcher{
		Client:  http.DefaultClient,
		PerHost: 50 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := f.Fetch(ctx, sources, store)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	for i := 1; i < len(requestTimes); i++ {
		diff := requestTimes[i].Sub(requestTimes[i-1])
		if diff < f.PerHost-5*time.Millisecond {
			t.Errorf("request %d was only %v after request %d, expected ≥%v",
				i, diff, i-1, f.PerHost)
		}
	}
}

func TestFetcher_304NotModified(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if etag := r.Header.Get("If-None-Match"); etag == "test-etag" {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write([]byte(rssFixture))
	}))
	defer ts.Close()

	store := &mockStateStore{}
	// Pre-populate ETag so conditional request fires.
	store.Put(context.Background(), "src", ts.URL, "test-etag", "", 200, "")

	f := &feed.Fetcher{Client: http.DefaultClient}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	results, err := f.Fetch(ctx, []config.Source{{Name: "src", URL: ts.URL}}, store)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	r := results[0]
	if !r.NotModified {
		t.Error("expected NotModified=true for 304 response")
	}
	if len(r.Items) != 0 {
		t.Errorf("expected zero items for 304, got %d", len(r.Items))
	}
	if r.RawPath != "" {
		t.Errorf("expected empty RawPath for 304, got %q", r.RawPath)
	}
}

func TestFetcher_200WithETag(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Header().Set("ETag", "new-etag")
		w.Header().Set("Last-Modified", "Mon, 01 Sep 2025 12:00:00 GMT")
		w.Write([]byte(rssFixture))
	}))
	defer ts.Close()

	store := &mockStateStore{}
	f := &feed.Fetcher{Client: http.DefaultClient}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	results, err := f.Fetch(ctx, []config.Source{{Name: "src", URL: ts.URL}}, store)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	r := results[0]
	if r.Err != nil {
		t.Fatalf("unexpected error: %v", r.Err)
	}
	if len(r.Items) == 0 {
		t.Fatal("expected items from 200 response")
	}

	// Verify StateStore was updated.
	etag, lm, err := store.Get(context.Background(), "src")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if etag != "new-etag" {
		t.Errorf("ETag in store: got %q, want %q", etag, "new-etag")
	}
	if lm == "" {
		t.Error("Last-Modified should be stored")
	}
}

func TestFetcher_500Error(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	sources := []config.Source{
		{Name: "good", URL: mustServerURL(t)},
		{Name: "bad", URL: ts.URL},
	}
	// We need a working server for the "good" source.
	goodTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write([]byte(rssFixture))
	}))
	defer goodTS.Close()

	sources[0].URL = goodTS.URL

	store := &mockStateStore{}
	f := &feed.Fetcher{Client: http.DefaultClient}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	results, err := f.Fetch(ctx, sources, store)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	// Good source should succeed.
	if results[0].Err != nil {
		t.Errorf("good source error: %v", results[0].Err)
	}

	// Bad source should have Result.Err, not a batch error.
	if results[1].Err == nil {
		t.Error("expected error for 500 source")
	}
}

func TestFetcher_malformedBody(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write([]byte("this is not valid xml at all <><>"))
	}))
	defer ts.Close()

	store := &mockStateStore{}
	f := &feed.Fetcher{Client: http.DefaultClient}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	results, err := f.Fetch(ctx, []config.Source{{Name: "bad", URL: ts.URL}}, store)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	r := results[0]
	if r.Err == nil {
		t.Error("expected error for malformed body")
	}
}

func TestFetcher_userAgent(t *testing.T) {
	var receivedUA string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write([]byte(rssFixture))
	}))
	defer ts.Close()

	store := &mockStateStore{}
	f := &feed.Fetcher{Client: http.DefaultClient}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	results, err := f.Fetch(ctx, []config.Source{{Name: "src", URL: ts.URL}}, store)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	if receivedUA == "" {
		t.Error("expected User-Agent header on request")
	}
	if results[0].Err != nil {
		t.Fatalf("unexpected error: %v", results[0].Err)
	}
}

func TestFetcher_conditionalHeaders(t *testing.T) {
	var gotIfNoneMatch, gotIfModifiedSince string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotIfNoneMatch = r.Header.Get("If-None-Match")
		gotIfModifiedSince = r.Header.Get("If-Modified-Since")
		w.WriteHeader(http.StatusNotModified)
	}))
	defer ts.Close()

	store := &mockStateStore{}
	store.Put(context.Background(), "src", ts.URL, "stored-etag", "Mon, 01 Sep 2025 12:00:00 GMT", 200, "")

	f := &feed.Fetcher{Client: http.DefaultClient}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	results, err := f.Fetch(ctx, []config.Source{{Name: "src", URL: ts.URL}}, store)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	if gotIfNoneMatch != "stored-etag" {
		t.Errorf("If-None-Match: got %q, want %q", gotIfNoneMatch, "stored-etag")
	}
	if gotIfModifiedSince != "Mon, 01 Sep 2025 12:00:00 GMT" {
		t.Errorf("If-Modified-Since: got %q, want %q", gotIfModifiedSince, "Mon, 01 Sep 2025 12:00:00 GMT")
	}

	if results[0].Err != nil {
		t.Fatalf("unexpected error: %v", results[0].Err)
	}
}

func TestFetcher_cacheFile(t *testing.T) {
	tmpDir := t.TempDir()
	cacheDir := filepath.Join(tmpDir, "cache")

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		body := []byte(rssFixture)
		w.Write(body)
	}))
	defer ts.Close()

	store := &mockStateStore{}
	f := &feed.Fetcher{
		Client:   http.DefaultClient,
		CacheDir: cacheDir,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	results, err := f.Fetch(ctx, []config.Source{{Name: "src", URL: ts.URL}}, store)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	r := results[0]
	if r.Err != nil {
		t.Fatalf("unexpected error: %v", r.Err)
	}
	if r.RawPath == "" {
		t.Fatal("expected non-empty RawPath for cache write")
	}

	// Verify the cache file exists and contains the exact bytes.
	data, err := os.ReadFile(r.RawPath)
	if err != nil {
		t.Fatalf("read cache file: %v", err)
	}
	if string(data) != rssFixture {
		t.Error("cache file does not contain exact response bytes")
	}

	// Verify path structure: <cacheDir>/<sha1(url)>/<YYYY-MM-DD>.xml
	dir := filepath.Dir(r.RawPath)
	base := filepath.Base(dir)
	if len(base) != 40 { // SHA-1 hex is 40 chars
		t.Errorf("cache dir name should be SHA-1 (40 hex chars), got %q", base)
	}
}

func TestFetcher_contextCancellation(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second) // block forever
		w.Write([]byte(rssFixture))
	}))
	defer ts.Close()

	store := &mockStateStore{}
	f := &feed.Fetcher{Client: http.DefaultClient}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	results, _ := f.Fetch(ctx, []config.Source{{Name: "slow", URL: ts.URL}}, store)
	// g.Wait() returns nil because goroutines swallow errors into Result.Err.
	// The cancellation is detected via the context.
	if ctx.Err() == nil {
		t.Error("expected context to be cancelled")
	}
	// Results should still be populated (one Result per source).
	if len(results) != 1 {
		t.Errorf("expected 1 result, got %d", len(results))
	}
	// The individual result should have an error from context deadline.
	if results[0].Err == nil {
		t.Error("expected per-source error for cancelled request")
	}
}

func TestFetcher_readabilityFallback(t *testing.T) {
	readabilityCalled := false
	var ts *httptest.Server

	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/article" {
			readabilityCalled = true
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(`<!DOCTYPE html><html><body><p>This is extracted article content with enough text to pass the 400 character threshold for readability processing. Lorem ipsum dolor sit amet, consectetur adipiscing elit. Sed do eiusmod tempor incididunt ut labore et dolore magna aliqua. Ut enim ad minim veniam, quis nostrud exercitation ullamco laboris nisi ut aliquip ex ea commodo consequat.</p></body></html>`))
			return
		}
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write([]byte(fmt.Sprintf(`<rss version="2.0">
  <channel><title>Test</title><link>%s</link>
    <item><title>Short Article</title><link>%s/article</link>
      <description>Short.</description>
      <pubDate>Mon, 01 Sep 2025 12:00:00 GMT</pubDate>
    </item></channel></rss>`, ts.URL, ts.URL)))
	}))
	defer ts.Close()

	store := &mockStateStore{}
	f := &feed.Fetcher{Client: http.DefaultClient}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	results, err := f.Fetch(ctx, []config.Source{{Name: "tier1", URL: ts.URL, Tier: 1}}, store)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	r := results[0]
	if r.Err != nil {
		t.Fatalf("unexpected error: %v", r.Err)
	}

	if !readabilityCalled {
		t.Error("expected readability fallback to be called for short-content tier-1 item")
	}
}
func TestFetcher_readabilitySkipsAggregator(t *testing.T) {
	readabilityCalled := false

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/article" {
			readabilityCalled = true
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(`<!DOCTYPE html><html><body><p>Extracted content</p></body></html>`))
			return
		}
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write([]byte(rssWithShortContent))
	}))
	defer ts.Close()

	store := &mockStateStore{}
	f := &feed.Fetcher{Client: http.DefaultClient}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Source URL is at the test server, but article link points to a different host (aggregator).
	results, err := f.Fetch(ctx, []config.Source{{Name: "agg", URL: ts.URL, Tier: 1}}, store)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	r := results[0]
	if r.Err != nil {
		t.Fatalf("unexpected error: %v", r.Err)
	}

	if readabilityCalled {
		t.Error("readability fallback should not fire for aggregator (link host differs from source host)")
	}
}

func TestFetcher_readabilitySkipsTier3(t *testing.T) {
	readabilityCalled := false

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/article" {
			readabilityCalled = true
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(`<!DOCTYPE html><html><body><p>Extracted content</p></body></html>`))
			return
		}
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write([]byte(rssWithShortContent))
	}))
	defer ts.Close()

	store := &mockStateStore{}
	f := &feed.Fetcher{Client: http.DefaultClient}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := f.Fetch(ctx, []config.Source{{Name: "tier3", URL: ts.URL, Tier: 3}}, store)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	if readabilityCalled {
		t.Error("readability fallback should not fire for tier-3 source")
	}
}

func TestFetcher_resultsInOrder(t *testing.T) {
	order := make([]string, 0)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		order = append(order, r.Header.Get("X-Source"))
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write([]byte(rssFixture))
	}))
	defer ts.Close()

	sources := []config.Source{
		{Name: "alpha", URL: ts.URL},
		{Name: "beta", URL: ts.URL},
		{Name: "gamma", URL: ts.URL},
	}

	store := &mockStateStore{}
	f := &feed.Fetcher{Client: http.DefaultClient}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	results, err := f.Fetch(ctx, sources, store)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	expectedOrder := []string{"alpha", "beta", "gamma"}
	for i, want := range expectedOrder {
		if results[i].Source.Name != want {
			t.Errorf("result[%d] source name: got %q, want %q", i, results[i].Source.Name, want)
		}
	}
}

// rssFixture is a minimal valid RSS 2.0 feed for testing.
const rssFixture = `<?xml version="1.0"?>
<rss version="2.0">
  <channel>
    <title>Test Feed</title>
    <link>https://example.com</link>
    <item>
      <title>Test Article</title>
      <link>https://example.com/article</link>
      <description>This is a test article with sufficient content length for parsing. It has enough words to be considered substantial content rather than a brief snippet or summary line.</description>
      <pubDate>Mon, 01 Sep 2025 12:00:00 GMT</pubDate>
      <guid isPermaLink="true">https://example.com/article</guid>
    </item>
  </channel>
</rss>`

// rssWithShortContent has an item with Content shorter than 400 chars.
const rssWithShortContent = `<?xml version="1.0"?>
<rss version="2.0">
  <channel>
    <title>Test Feed</title>
    <link>https://example.com</link>
    <item>
      <title>Short Article</title>
      <link>https://other-host.example.com/article</link>
      <description>Short content.</description>
      <pubDate>Mon, 01 Sep 2025 12:00:00 GMT</pubDate>
      <guid isPermaLink="true">https://example.com/short</guid>
    </item>
  </channel>
</rss>`

// mustServerURL returns the URL of an httptest server, failing the test on error.
func mustServerURL(t *testing.T) string {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write([]byte(rssFixture))
	}))
	t.Cleanup(ts.Close)
	return ts.URL
}

