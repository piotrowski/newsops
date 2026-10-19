//go:build e2e

// Package e2e drives the built newsops binary through every subcommand
// against local fixture feeds. Run with `make e2e`.
package e2e

import (
	"bytes"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// env is the shared state of one end-to-end run: binary, temp dirs, feeds.
type env struct {
	t       *testing.T
	bin     string
	dbDir   string
	cfgDir  string
	digest  string
	feeds   *httptest.Server
	deadURL string
	serve   map[string]string // path → body
}

func newEnv(t *testing.T) *env {
	t.Helper()
	bin := os.Getenv("NEWSOPS_BIN")
	if bin == "" {
		bin = filepath.Join("..", "newsops")
	}
	abs, err := filepath.Abs(bin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(abs); err != nil {
		t.Fatalf("binary %s not found; run `make build` first (or set NEWSOPS_BIN)", abs)
	}

	root := t.TempDir()
	e := &env{
		t:      t,
		bin:    abs,
		dbDir:  filepath.Join(root, "data"),
		cfgDir: filepath.Join(root, "config"),
		digest: filepath.Join(root, "digest.md"),
		serve:  map[string]string{},
	}
	e.feeds = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := e.serve[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		etag := fmt.Sprintf(`"%x"`, sha1.Sum([]byte(body)))
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		fmt.Fprint(w, body)
	}))
	t.Cleanup(e.feeds.Close)

	// A port nothing listens on: open then close a listener.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	e.deadURL = "http://" + l.Addr().String() + "/none.xml"
	l.Close()

	if err := os.MkdirAll(e.cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	e.writeConfig(e.feeds.URL)
	return e
}

func (e *env) writeConfig(base string) {
	sources := fmt.Sprintf(`sources:
  - {name: FeedA, tier: 1, url: %s/a.xml}
  - {name: hacker-news-digest, tier: 2, url: %s/hn.xml}
  - {name: Dead, tier: 3, url: %s}
`, base, base, e.deadURL)
	mustWrite(e.t, filepath.Join(e.cfgDir, "sources.yml"), sources)
	mustWrite(e.t, filepath.Join(e.cfgDir, "domains.yml"), "primary_domains: [example.com]\n")
	mustWrite(e.t, filepath.Join(e.cfgDir, "prefilter.yml"), "candidate_limit: 40\nper_source_cap: 3\nexclude_terms: [sponsored]\n")
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func rssItem(title, link, comments, desc string) string {
	// Published "now": the fetch window starts at the last successful fetch,
	// so an item dated earlier than that would be dropped as old.
	pub := time.Now().UTC().Format(time.RFC1123)
	c := ""
	if comments != "" {
		c = "<comments>" + comments + "</comments>"
	}
	return fmt.Sprintf(`<item><title>%s</title><link>%s</link>%s<pubDate>%s</pubDate><description>%s</description></item>`,
		title, link, c, pub, desc)
}

func rss(items ...string) string {
	return `<?xml version="1.0"?><rss version="2.0"><channel><title>T</title>` + strings.Join(items, "") + `</channel></rss>`
}

// run executes the binary with --db/--config/--digest and --json, returning
// parsed stdout (nil when not JSON), raw stdout+stderr, and the exit code.
func (e *env) run(args ...string) (map[string]any, string, int) {
	e.t.Helper()
	full := append([]string{"--db", e.dbDir, "--config", e.cfgDir, "--digest", e.digest, "--json"}, args...)
	cmd := exec.Command(e.bin, full...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		e.t.Fatalf("run %v: %v", args, err)
	}
	var parsed map[string]any
	if json.Unmarshal(out.Bytes(), &parsed) != nil {
		parsed = nil
	}
	e.t.Logf("$ newsops %s → exit %d\n%s%s", strings.Join(args, " "), code, truncate(out.String()), errb.String())
	return parsed, out.String() + errb.String(), code
}

func truncate(s string) string {
	if len(s) > 400 {
		return s[:400] + "…\n"
	}
	return s
}

// get walks a dotted path ("stages.fetch.result.items.new") through nested
// maps and returns the leaf.
func get(m map[string]any, path string) any {
	var cur any = m
	for _, k := range strings.Split(path, ".") {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[k]
	}
	return cur
}

func num(v any) float64 {
	f, _ := v.(float64)
	return f
}

func (e *env) expectCode(got, want int, what string) {
	e.t.Helper()
	if got != want {
		e.t.Errorf("%s: exit %d, want %d", what, got, want)
	}
}

func (e *env) expectNum(m map[string]any, path string, want float64) {
	e.t.Helper()
	if got := num(get(m, path)); got != want {
		e.t.Errorf("%s = %v, want %v", path, get(m, path), want)
	}
}

func (e *env) expectStr(m map[string]any, path, want string) {
	e.t.Helper()
	if got, _ := get(m, path).(string); got != want {
		e.t.Errorf("%s = %q, want %q", path, get(m, path), want)
	}
}

func TestEndToEnd(t *testing.T) {
	e := newEnv(t)
	e.serve["/a.xml"] = rss(
		rssItem("Cloudflare postmortem on the big outage of today", "https://example.com/a1?utm_source=x", "", "Body one about the outage"),
		rssItem("Postgres 18 ships async IO for everyone", "https://example.com/a2", "", "Body two about postgres"),
		rssItem("Sponsored: buy our thing", "https://example.com/ad", "", "advert"),
	)
	e.serve["/hn.xml"] = rss(
		rssItem("Postgres 18 ships async IO for everyone", "https://www.example.com/a2/", "https://news.ycombinator.com/item?id=1", "hn"),
	)

	t.Run("version", func(t *testing.T) {
		m, _, code := e.run("version")
		e.expectCode(code, 0, "version")
		if get(m, "version") == nil || get(m, "commit") == nil {
			t.Errorf("version output missing keys: %v", m)
		}
	})

	t.Run("no args and unknown command", func(t *testing.T) {
		_, _, code := e.run()
		e.expectCode(code, 2, "no args")
		m, _, code := e.run("nonsense")
		e.expectCode(code, 2, "unknown command")
		e.expectStr(m, "error", "unknown_command")
		m, _, code = e.run("query", "--bogus")
		e.expectCode(code, 2, "unknown flag")
		e.expectStr(m, "error", "invalid_argument")
	})

	t.Run("status before any run creates nothing", func(t *testing.T) {
		m, _, code := e.run("status")
		e.expectCode(code, 0, "status")
		if get(m, "stages.fetch.stale") != true {
			t.Errorf("fetch should be stale on a virgin db: %v", m)
		}
		if _, err := os.Stat(e.dbDir); !os.IsNotExist(err) {
			t.Errorf("status must not create the data dir")
		}
		_, _, code = e.run("show", "1")
		e.expectCode(code, 2, "show on missing db")
		if _, err := os.Stat(e.dbDir); !os.IsNotExist(err) {
			t.Errorf("read-only command must not create the data dir")
		}
	})

	t.Run("sources:check", func(t *testing.T) {
		m, _, code := e.run("sources:check")
		e.expectCode(code, 1, "sources:check with one dead feed")
		e.expectNum(m, "passed", 2)
		e.expectNum(m, "failed", 1)
		m, _, code = e.run("sources:check", "--source", "FeedA")
		e.expectCode(code, 0, "sources:check --source")
		e.expectNum(m, "passed", 1)
		_, _, code = e.run("sources:check", "--source", "Nope")
		e.expectCode(code, 2, "sources:check unknown source")
	})

	t.Run("fetch", func(t *testing.T) {
		m, _, code := e.run("fetch")
		e.expectCode(code, 1, "fetch partial")
		e.expectStr(m, "error", "partial_fetch")
		e.expectNum(m, "items.new", 4)
		e.expectNum(m, "sources.ok", 2)
		e.expectNum(m, "sources.failed", 1)

		m, _, code = e.run("fetch", "--max-age", "1h")
		e.expectCode(code, 0, "fetch fresh")
		if get(m, "skipped") != true {
			t.Errorf("second fetch within max-age should skip: %v", m)
		}

		m, _, code = e.run("fetch", "--source", "FeedA")
		e.expectCode(code, 0, "fetch one source")
		e.expectNum(m, "sources.not_modified", 1)
		e.expectNum(m, "items.new", 0)
	})

	t.Run("dedupe", func(t *testing.T) {
		m, _, code := e.run("dedupe")
		e.expectCode(code, 0, "dedupe")
		e.expectNum(m, "items_in", 4)
		e.expectNum(m, "stories_created", 3)
		e.expectNum(m, "stories_updated", 1)
		e.expectNum(m, "methods.url", 1)
		e.expectNum(m, "discussions_linked", 1)

		m, _, _ = e.run("dedupe", "--window", "1h")
		e.expectNum(m, "items_in", 0)
	})

	t.Run("candidates", func(t *testing.T) {
		m, _, code := e.run("candidates")
		e.expectCode(code, 0, "candidates")
		e.expectNum(m, "total_in_window", 3)
		e.expectNum(m, "returned", 2)
		e.expectNum(m, "dropped.excluded", 1)
		list, _ := get(m, "candidates").([]any)
		if len(list) != 2 || num(get(list[0].(map[string]any), "source_count")) != 2 {
			t.Errorf("expected the two-source story first: %v", list)
		}
		m, _, _ = e.run("candidates", "--limit", "1")
		e.expectNum(m, "returned", 1)
		m, _, _ = e.run("candidates", "--since", time.Now().UTC().Add(time.Hour).Format(time.RFC3339))
		e.expectNum(m, "total_in_window", 0)
	})

	t.Run("show", func(t *testing.T) {
		m, _, code := e.run("show", "2")
		e.expectCode(code, 0, "show")
		e.expectNum(m, "story.source_count", 2)
		items, _ := get(m, "items").([]any)
		if len(items) != 2 {
			t.Errorf("story 2 should have 2 items, got %d", len(items))
		}
		var discussion bool
		srcs, _ := get(m, "sources").([]any)
		for _, s := range srcs {
			if get(s.(map[string]any), "kind") == "discussion" {
				discussion = true
			}
		}
		if !discussion {
			t.Errorf("story 2 should carry an HN discussion link: %v", srcs)
		}
		_, _, code = e.run("show", "999")
		e.expectCode(code, 2, "show unknown")
	})

	t.Run("query", func(t *testing.T) {
		m, _, code := e.run("query", "--text", "postgres async")
		e.expectCode(code, 0, "query text")
		e.expectNum(m, "total", 1)
		res, _ := get(m, "results").([]any)
		if len(res) != 1 || !strings.Contains(res[0].(map[string]any)["snippet"].(string), "<b>Postgres</b>") {
			t.Errorf("snippet should highlight the match: %v", res)
		}
		m, _, _ = e.run("query", "--domain", "example.com", "--limit", "1")
		e.expectNum(m, "total", 3)
		if res, _ := get(m, "results").([]any); len(res) != 1 {
			t.Errorf("--limit 1 should return one result, got %d", len(res))
		}
		m, _, _ = e.run("query", "--story", "2", "--related")
		if num(get(m, "total")) < 1 {
			t.Errorf("related query should find at least the story itself: %v", m)
		}
		_, _, code = e.run("query")
		e.expectCode(code, 2, "query without criteria")
	})

	t.Run("decide", func(t *testing.T) {
		m, _, code := e.run("decide", "1,2", "shown", "--note", "rank=1")
		e.expectCode(code, 0, "decide comma list")
		if applied, _ := get(m, "applied").([]any); len(applied) != 2 {
			t.Errorf("applied = %v, want [1 2]", applied)
		}
		m, _, _ = e.run("decide", "1", "3", "shown")
		if as, _ := get(m, "already_shown").([]any); len(as) != 1 || num(as[0]) != 1 {
			t.Errorf("already_shown = %v, want [1]", as)
		}
		m, _, code = e.run("decide", "2", "999", "briefed")
		e.expectCode(code, 2, "decide unknown id")
		e.expectStr(m, "error", "unknown_story")
		_, _, code = e.run("decide", "2", "frobnicate")
		e.expectCode(code, 2, "decide invalid action")

		m, _, _ = e.run("show", "1")
		decs, _ := get(m, "decisions").([]any)
		if len(decs) != 1 || get(decs[0].(map[string]any), "note") != "rank=1" {
			t.Errorf("decision note not recorded: %v", decs)
		}
	})

	t.Run("candidates after shown", func(t *testing.T) {
		m, _, _ := e.run("candidates")
		e.expectNum(m, "returned", 0)
		e.expectNum(m, "dropped.shown", 3)
		m, _, _ = e.run("candidates", "--include-shown")
		e.expectNum(m, "returned", 2)
	})

	t.Run("status after runs", func(t *testing.T) {
		mustWrite(t, e.digest, "# Digest\n")
		m, _, code := e.run("status")
		e.expectCode(code, 0, "status")
		e.expectNum(m, "counts.items_total", 4)
		e.expectNum(m, "counts.unshown_stories", 0)
		if get(m, "stages.fetch.stale") != false || get(m, "stages.dedupe.stale") != false {
			t.Errorf("stages should be fresh: %v", get(m, "stages"))
		}
		if get(m, "digest.generated_at") == nil {
			t.Errorf("digest generated_at should be set once digest.md exists")
		}
	})

	t.Run("collect", func(t *testing.T) {
		m, _, code := e.run("collect")
		e.expectCode(code, 0, "collect fresh")
		if get(m, "stages.fetch.skipped") != true || get(m, "stages.dedupe.skipped") != true {
			t.Errorf("fresh collect should skip fetch and dedupe: %v", get(m, "stages"))
		}

		e.serve["/a.xml"] = rss(
			rssItem("Cloudflare postmortem on the big outage of today", "https://example.com/a1?utm_source=x", "", "Body one about the outage"),
			rssItem("Brand new story about kernels", "https://example.com/a3", "", "Body three"),
		)
		m, _, code = e.run("collect", "--max-age", "0s")
		e.expectCode(code, 1, "collect with one dead feed")
		e.expectNum(m, "exit", 1)
		e.expectNum(m, "stages.fetch.result.items.new", 1)
		e.expectNum(m, "stages.fetch.result.sources.failed", 1)
		e.expectNum(m, "stages.dedupe.result.stories_created", 1)
		list, _ := get(m, "candidates").([]any)
		if len(list) != 1 || get(list[0].(map[string]any), "title") != "Brand new story about kernels" {
			t.Errorf("collect should surface only the new story: %v", list)
		}
	})

	t.Run("fetch outage does not advance the window", func(t *testing.T) {
		before, _, _ := e.run("status")
		e.writeConfig("http://" + strings.TrimSuffix(strings.TrimPrefix(e.deadURL, "http://"), "/none.xml"))
		m, _, code := e.run("fetch", "--max-age", "0s")
		e.expectCode(code, 2, "fetch all dead")
		e.expectNum(m, "sources.ok", 0)
		after, _, _ := e.run("status")
		if get(before, "stages.fetch.last_success") != get(after, "stages.fetch.last_success") {
			t.Errorf("all-failed fetch must not count as success: %v → %v",
				get(before, "stages.fetch.last_success"), get(after, "stages.fetch.last_success"))
		}
		e.writeConfig(e.feeds.URL)
	})

	t.Run("write lock", func(t *testing.T) {
		lock := filepath.Join(e.dbDir, "news.lock")
		holder := exec.Command("flock", lock, "sleep", "2")
		if err := holder.Start(); err != nil {
			t.Skipf("flock not available: %v", err)
		}
		defer holder.Wait()
		time.Sleep(300 * time.Millisecond)
		m, _, code := e.run("dedupe")
		e.expectCode(code, 2, "dedupe under foreign lock")
		e.expectStr(m, "error", "locked")
		_, _, code = e.run("status")
		e.expectCode(code, 0, "status under lock stays readable")
	})
}
