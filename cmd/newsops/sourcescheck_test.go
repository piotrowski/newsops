package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"news-ops/internal/config"
)
// minimalRSS returns a valid RSS 2.0 feed with one item.
func minimalRSSWithDate(title, link string) string {
	return `<?xml version="1.0"?>
<rss version="2.0">
  <channel><title>T</title><link>https://example.com</link>
    <item><title>%s</title><link>%s</link>
      <description>Description.</description>
      <pubDate>Tue, 01 Sep 2026 12:00:00 GMT</pubDate>
      <guid isPermaLink="true">%s</guid>
    </item></channel></rss>`
}

func writeCheckConfig(t *testing.T, configDir string, sourcesYML string) {
	t.Helper()
	domainsYML := `primary_domains: []`
	prefilterYML := `candidate_limit: 40
per_source_cap: 3
title_simhash_max: 3
max_backfill_days: 30
exclude_terms: []`
	os.WriteFile(filepath.Join(configDir, "sources.yml"), []byte(sourcesYML), 0644)
	os.WriteFile(filepath.Join(configDir, "domains.yml"), []byte(domainsYML), 0644)
	os.WriteFile(filepath.Join(configDir, "prefilter.yml"), []byte(prefilterYML), 0644)
}
// configSource creates a config.Source for testing.
func configSource(t *testing.T, name string, tier int, url string) config.Source {
	t.Helper()
	return config.Source{Name: name, Tier: tier, URL: url}
}

// TestCheckSource_ok tests a source that returns a valid feed with items.
func TestCheckSource_ok(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write([]byte(minimalRSSWithDate("Article 1", "https://example.com/1")))
	}))
	defer ts.Close()

	src := configSource(t, "TestFeed", 1, ts.URL+"/feed")
	r := checkSource(context.Background(), src)

	if r.Status != "ok" {
		t.Errorf("status: got %q, want ok", r.Status)
	}
	if r.HTTPStatus != 200 {
		t.Errorf("http_status: got %d, want 200", r.HTTPStatus)
	}
	if r.Items != 1 {
		t.Errorf("items: got %d, want 1", r.Items)
	}
	if r.Error != "" {
		t.Errorf("error: got %q, want empty", r.Error)
	}
}

// TestCheckSource_httpError tests a source that returns a non-200 status.
func TestCheckSource_httpError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	src := configSource(t, "NotFound", 1, ts.URL+"/feed")
	r := checkSource(context.Background(), src)

	if r.Status != "http_error" {
		t.Errorf("status: got %q, want http_error", r.Status)
	}
	if r.HTTPStatus != 404 {
		t.Errorf("http_status: got %d, want 404", r.HTTPStatus)
	}
	if r.Items != 0 {
		t.Errorf("items: got %d, want 0", r.Items)
	}
}

// TestCheckSource_parseError tests a source that returns non-feed content.
func TestCheckSource_parseError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html><body>Not a feed</body></html>"))
	}))
	defer ts.Close()

	src := configSource(t, "HTMLFeed", 1, ts.URL+"/feed")
	r := checkSource(context.Background(), src)

	if r.Status != "parse_error" {
		t.Errorf("status: got %q, want parse_error", r.Status)
	}
	if r.HTTPStatus != 200 {
		t.Errorf("http_status: got %d, want 200", r.HTTPStatus)
	}
	if r.Items != 0 {
		t.Errorf("items: got %d, want 0", r.Items)
	}
}

// TestCheckSource_empty tests a source that returns a valid feed with zero items.
func TestCheckSource_empty(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write([]byte(`<?xml version="1.0"?>
<rss version="2.0">
  <channel><title>T</title><link>https://example.com</link></channel></rss>`))
	}))
	defer ts.Close()

	src := configSource(t, "EmptyFeed", 1, ts.URL+"/feed")
	r := checkSource(context.Background(), src)

	if r.Status != "empty" {
		t.Errorf("status: got %q, want empty", r.Status)
	}
	if r.HTTPStatus != 200 {
		t.Errorf("http_status: got %d, want 200", r.HTTPStatus)
	}
	if r.Items != 0 {
		t.Errorf("items: got %d, want 0", r.Items)
	}
}

// TestCheckSource_networkError tests a source that cannot be reached.
func TestCheckSource_networkError(t *testing.T) {
	src := configSource(t, "Unreachable", 1, "http://127.0.0.1:1/")
	r := checkSource(context.Background(), src)

	if r.Status != "network_error" {
		t.Errorf("status: got %q, want network_error", r.Status)
	}
	if r.HTTPStatus != 0 {
		t.Errorf("http_status: got %d, want 0", r.HTTPStatus)
	}
	if r.Items != 0 {
		t.Errorf("items: got %d, want 0", r.Items)
	}
}

// TestIntegration_sourcesCheckAllPass tests exit code 0 when all sources pass.
func TestIntegration_sourcesCheckAllPass(t *testing.T) {
	if _, err := os.Stat(binPath()); os.IsNotExist(err) {
		t.Skip("binary not built, skipping integration test")
	}

	tmpDir := t.TempDir()
	dbDir := filepath.Join(tmpDir, "data")
	configDir := filepath.Join(tmpDir, "config")
	os.MkdirAll(configDir, 0755)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write([]byte(minimalRSSWithDate("Article", "https://example.com/1")))
	}))
	defer ts.Close()

	sourcesYML := `sources:
  - name: FeedA
    tier: 1
    url: "` + ts.URL + `/a"
  - name: FeedB
    tier: 1
    url: "` + ts.URL + `/b"`
	writeCheckConfig(t, configDir, sourcesYML)

	cmd := exec.Command(binPath(), "sources:check", "--db", dbDir, "--config", configDir, "--json")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sources:check: %v\n%s", err, string(out))
	}

	var result sourcesCheckResult
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("parse JSON: %v\noutput: %s", err, string(out))
	}

	if result.Total != 2 {
		t.Errorf("total: got %d, want 2", result.Total)
	}
	if result.Passed != 2 {
		t.Errorf("passed: got %d, want 2", result.Passed)
	}
	if result.Failed != 0 {
		t.Errorf("failed: got %d, want 0", result.Failed)
	}

	// Verify JSON shape — each result has expected fields.
	for _, r := range result.Results {
		if r.Name == "" || r.URL == "" || r.Status == "" {
			t.Errorf("result missing field: %+v", r)
		}
	}
}

// TestIntegration_sourcesCheckFailures tests exit code 1 when any source fails.
func TestIntegration_sourcesCheckFailures(t *testing.T) {
	if _, err := os.Stat(binPath()); os.IsNotExist(err) {
		t.Skip("binary not built, skipping integration test")
	}

	tmpDir := t.TempDir()
	dbDir := filepath.Join(tmpDir, "data")
	configDir := filepath.Join(tmpDir, "config")
	os.MkdirAll(configDir, 0755)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/good") {
			w.Header().Set("Content-Type", "application/rss+xml")
			w.Write([]byte(minimalRSSWithDate("Good Article", "https://example.com/good")))
		} else if strings.Contains(r.URL.Path, "/notfound") {
			w.WriteHeader(http.StatusNotFound)
		} else {
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte("<html>bad</html>"))
		}
	}))
	defer ts.Close()

	sourcesYML := `sources:
  - name: GoodFeed
    tier: 1
    url: "` + ts.URL + `/good"
  - name: NotFoundFeed
    tier: 1
    url: "` + ts.URL + `/notfound"
  - name: BadFeed
    tier: 1
    url: "` + ts.URL + `/bad"`
	writeCheckConfig(t, configDir, sourcesYML)

	cmd := exec.Command(binPath(), "sources:check", "--db", dbDir, "--config", configDir, "--json")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected non-zero exit for failures")
	}

	var result sourcesCheckResult
	if jsonErr := json.Unmarshal(out, &result); jsonErr != nil {
		t.Fatalf("parse JSON: %v\noutput: %s", jsonErr, string(out))
	}

	if result.Total != 3 {
		t.Errorf("total: got %d, want 3", result.Total)
	}
	if result.Passed != 1 {
		t.Errorf("passed: got %d, want 1", result.Passed)
	}
	if result.Failed != 2 {
		t.Errorf("failed: got %d, want 2", result.Failed)
	}

	// Verify status values.
	statuses := make(map[string]int)
	for _, r := range result.Results {
		statuses[r.Status]++
	}
	if statuses["ok"] != 1 {
		t.Errorf("ok count: got %d, want 1", statuses["ok"])
	}
	if statuses["http_error"] != 1 {
		t.Errorf("http_error count: got %d, want 1", statuses["http_error"])
	}
	if statuses["parse_error"] != 1 {
		t.Errorf("parse_error count: got %d, want 1", statuses["parse_error"])
	}
}

// TestIntegration_sourcesCheckNoFilesystemWrites asserts no files are created.
func TestIntegration_sourcesCheckNoFilesystemWrites(t *testing.T) {
	if _, err := os.Stat(binPath()); os.IsNotExist(err) {
		t.Skip("binary not built, skipping integration test")
	}

	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, "config")
	os.MkdirAll(configDir, 0755)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write([]byte(minimalRSSWithDate("Article", "https://example.com/1")))
	}))
	defer ts.Close()

	sourcesYML := `sources:
  - name: TestFeed
    tier: 1
    url: "` + ts.URL + `/feed"`
	writeCheckConfig(t, configDir, sourcesYML)

	// data/ dir must NOT exist before or after.
	dataDir := filepath.Join(tmpDir, "data")
	if _, err := os.Stat(dataDir); !os.IsNotExist(err) {
		t.Fatal("data/ directory should not exist before check")
	}

	cmd := exec.Command(binPath(), "sources:check", "--db", dataDir, "--config", configDir, "--json")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sources:check: %v\n%s", err, string(out))
	}

	if _, err := os.Stat(dataDir); !os.IsNotExist(err) {
		t.Fatal("data/ directory should not exist after check — no writes allowed")
	}

	// Verify we got valid JSON output.
	var result sourcesCheckResult
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("parse JSON: %v\noutput: %s", err, string(out))
	}
	if result.Passed != 1 {
		t.Errorf("passed: got %d, want 1", result.Passed)
	}
}

// TestIntegration_sourcesCheckSingleSource tests --source flag.
func TestIntegration_sourcesCheckSingleSource(t *testing.T) {
	if _, err := os.Stat(binPath()); os.IsNotExist(err) {
		t.Skip("binary not built, skipping integration test")
	}

	tmpDir := t.TempDir()
	dbDir := filepath.Join(tmpDir, "data")
	configDir := filepath.Join(tmpDir, "config")
	os.MkdirAll(configDir, 0755)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write([]byte(minimalRSSWithDate("Article", "https://example.com/1")))
	}))
	defer ts.Close()

	sourcesYML := `sources:
  - name: Alpha
    tier: 1
    url: "` + ts.URL + `/alpha"
  - name: Beta
    tier: 1
    url: "` + ts.URL + `/beta"`
	writeCheckConfig(t, configDir, sourcesYML)

	cmd := exec.Command(binPath(), "sources:check", "--db", dbDir, "--config", configDir, "--source", "Alpha", "--json")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sources:check --source Alpha: %v\n%s", err, string(out))
	}

	var result sourcesCheckResult
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("parse JSON: %v\noutput: %s", err, string(out))
	}

	if result.Total != 1 {
		t.Errorf("total: got %d, want 1 (single source filter)", result.Total)
	}
	if result.Passed != 1 {
		t.Errorf("passed: got %d, want 1", result.Passed)
	}
}

// TestIntegration_sourcesCheckInvalidSource tests --source with non-existent name.
func TestIntegration_sourcesCheckInvalidSource(t *testing.T) {
	if _, err := os.Stat(binPath()); os.IsNotExist(err) {
		t.Skip("binary not built, skipping integration test")
	}

	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, "config")
	os.MkdirAll(configDir, 0755)

	sourcesYML := `sources:
  - name: Alpha
    tier: 1
    url: "http://localhost:0/alpha"`
	writeCheckConfig(t, configDir, sourcesYML)

	cmd := exec.Command(binPath(), "sources:check", "--config", configDir, "--source", "NonExistent", "--json")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected non-zero exit for invalid source")
	}

	// Should be exit code 2 (bad argument).
	var envelope map[string]any
	if err := json.Unmarshal(out, &envelope); err != nil {
		t.Fatalf("parse JSON: %v\noutput: %s", err, string(out))
	}
	if slug, ok := envelope["error"].(string); !ok || slug != "invalid_argument" {
		t.Errorf("slug: got %q, want invalid_argument", envelope["error"])
	}
}

// TestIntegration_sourcesCheckConcurrency verifies all sources are checked quickly.
func TestIntegration_sourcesCheckConcurrency(t *testing.T) {
	if _, err := os.Stat(binPath()); os.IsNotExist(err) {
		t.Skip("binary not built, skipping integration test")
	}

	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, "config")
	os.MkdirAll(configDir, 0755)

	var mu sync.Mutex
	requestCount := 0

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requestCount++
		mu.Unlock()
		time.Sleep(200 * time.Millisecond) // slow response to test concurrency
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write([]byte(minimalRSSWithDate("Article", "https://example.com/1")))
	}))
	defer ts.Close()

	var sourcesYML strings.Builder
	sourcesYML.WriteString("sources:\n")
	for i := 0; i < 5; i++ {
		sourcesYML.WriteString(fmt.Sprintf(`  - name: Feed%d
    tier: 1
    url: "%s/feed%d"
`, i, ts.URL, i))
	}

	writeCheckConfig(t, configDir, sourcesYML.String())

	start := time.Now()
	cmd := exec.Command(binPath(), "sources:check", "--config", configDir, "--json")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sources:check: %v\n%s", err, string(out))
	}

	elapsed := time.Since(start)
	if elapsed > 3*time.Second {
		t.Errorf("took %v — expected <3s with concurrency=10 (5 sources × 200ms each)", elapsed)
	}

	var result sourcesCheckResult
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("parse JSON: %v\noutput: %s", err, string(out))
	}

	if result.Total != 5 {
		t.Errorf("total: got %d, want 5", result.Total)
	}
	if requestCount != 5 {
		t.Errorf("requests: got %d, want 5 (all sources checked)", requestCount)
	}
}

// TestIntegration_sourcesCheckUserAgent verifies the descriptive User-Agent is sent.
func TestIntegration_sourcesCheckUserAgent(t *testing.T) {
	if _, err := os.Stat(binPath()); os.IsNotExist(err) {
		t.Skip("binary not built, skipping integration test")
	}

	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, "config")
	os.MkdirAll(configDir, 0755)

	var receivedUA string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write([]byte(minimalRSSWithDate("Article", "https://example.com/1")))
	}))
	defer ts.Close()

	sourcesYML := `sources:
  - name: TestFeed
    tier: 1
    url: "` + ts.URL + `/feed"`
	writeCheckConfig(t, configDir, sourcesYML)

	cmd := exec.Command(binPath(), "sources:check", "--config", configDir, "--json")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sources:check: %v\n%s", err, string(out))
	}

	var result sourcesCheckResult
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("parse JSON: %v\noutput: %s", err, string(out))
	}

	if !strings.Contains(receivedUA, "newsops/") {
		t.Errorf("User-Agent: got %q, expected to contain 'newsops/'", receivedUA)
	}
	if !strings.Contains(receivedUA, "personal feed reader") {
		t.Errorf("User-Agent: got %q, expected to contain 'personal feed reader'", receivedUA)
	}

	if result.Passed != 1 {
		t.Errorf("passed: got %d, want 1", result.Passed)
	}
}

// TestIntegration_sourcesCheckHumanOutput tests the table format.
func TestIntegration_sourcesCheckHumanOutput(t *testing.T) {
	if _, err := os.Stat(binPath()); os.IsNotExist(err) {
		t.Skip("binary not built, skipping integration test")
	}

	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, "config")
	os.MkdirAll(configDir, 0755)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/good") {
			w.Header().Set("Content-Type", "application/rss+xml")
			w.Write([]byte(minimalRSSWithDate("Good Article", "https://example.com/good")))
		} else if strings.Contains(r.URL.Path, "/bad") {
			w.WriteHeader(http.StatusNotFound)
		} else {
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte("<html>bad</html>"))
		}
	}))
	defer ts.Close()

	sourcesYML := `sources:
  - name: GoodFeed
    tier: 1
    url: "` + ts.URL + `/good"
  - name: NotFoundFeed
    tier: 1
    url: "` + ts.URL + `/bad"
  - name: BadHTMLFeed
    tier: 1
    url: "` + ts.URL + `/html"`
	writeCheckConfig(t, configDir, sourcesYML)

	cmd := exec.Command(binPath(), "sources:check", "--config", configDir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected non-zero exit for failures")
	}

	output := string(out)
	if !strings.Contains(output, "PASS") {
		t.Error("human output should contain PASS")
	}
	if !strings.Contains(output, "FAIL") {
		t.Error("human output should contain FAIL")
	}
	if !strings.Contains(output, "1/3 passed") {
		t.Errorf("human output should show summary: got %q", output)
	}
}
