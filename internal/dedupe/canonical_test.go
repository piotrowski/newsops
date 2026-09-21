package dedupe

import (
	"context"
	"testing"

	"news-ops/internal/feed"
	"news-ops/internal/config"
)

// -----------------------------------------------------------------------
// Canonical table test — ~25 cases covering every rule and acceptance line

func TestCanonical(t *testing.T) {
	tests := []struct {
		in, want string
		err      bool
	}{
		// --- Acceptance lines from T010 ---
		{
			in:   "https://ex.com/a?utm_source=x&id=7#frag",
			want: "https://ex.com/a?id=7",
		},
		{
			in:   "https://WWW.Ex.com/A/",
			want: "https://ex.com/A", // path case preserved
		},
		{
			in:   "https://ex.com:443/a",
			want: "https://ex.com/a",
		},
		{
			in:   "https://ex.com/",
			want: "https://ex.com/", // bare slash kept
		},

		// --- Rule 2: lowercase scheme + host, strip www. ---
		{
			in:   "HTTP://EXAMPLE.COM/path",
			want: "http://example.com/path",
		},
		{
			in:   "https://www.sub.example.com/a",
			want: "https://sub.example.com/a",
		},

		// --- Rule 3: fragment dropped ---
		{
			in:   "https://ex.com/path#section-2",
			want: "https://ex.com/path",
		},
		{
			in:   "https://ex.com/path#",
			want: "https://ex.com/path",
		},

		// --- Rule 4: tracking params removed ---
		{
			in:   "https://ex.com/page?utm_medium=email&utm_campaign=spring&ref=foo",
			want: "https://ex.com/page",
		},
		{
			in:   "https://ex.com/page?fbclid=abc123&gclid=xyz",
			want: "https://ex.com/page",
		},
		{
			in:   "https://ex.com/page?mc_cid=test&mc_eid=data&_hsenc=x",
			want: "https://ex.com/page",
		},
		{
			in:   "https://ex.com/page?spm=123&at_medium=web",
			want: "https://ex.com/page",
		},

		// --- Rule 5: query params sorted ---
		{
			in:   "https://ex.com/page?b=2&a=1&c=3",
			want: "https://ex.com/page?a=1&b=2&c=3",
		},
		{
			in:   "https://ex.com/page?z=1&a=1",
			want: "https://ex.com/page?a=1&z=1",
		},

		// --- Rule 6: trailing slash stripped (except bare /) ---
		{
			in:   "https://ex.com/path/",
			want: "https://ex.com/path",
		},
		{
			in:   "https://ex.com/a/b/c/",
			want: "https://ex.com/a/b/c",
		},

		// --- Rule 7: default port dropped ---
		{
			in:   "http://ex.com:80/path",
			want: "http://ex.com/path",
		},
		{
			in:   "https://ex.com:443/path",
			want: "https://ex.com/path",
		},

		// --- Non-http(s) rejected ---
		{
			in:   "mailto:user@example.com",
			err:  true,
		},
		{
			in:   "ftp://files.example.com/doc.pdf",
			err:  true,
		},
		{
			in:   "file:///etc/passwd",
			err:  true,
		},

		// --- Non-default port preserved ---
		{
			in:   "https://ex.com:8443/api",
			want: "https://ex.com:8443/api",
		},
		{
			in:   "http://ex.com:9000/debug",
			want: "http://ex.com:9000/debug",
		},

		// --- Mixed tracking + real params ---
		{
			in:   "https://ex.com/article?id=42&utm_source=newsletter&sort=date",
			want: "https://ex.com/article?id=42&sort=date",
		},

		// --- Multiple values for same key ---
		{
			in:   "https://ex.com/search?q=go&q=rust",
			want: "https://ex.com/search?q=go&q=rust",
		},

		// --- Path case preserved (not lowercased) ---
		{
			in:   "https://example.com/Go-Pro/Article-One",
			want: "https://example.com/Go-Pro/Article-One",
		},

		// --- www. on subdomain ---
		{
			in:   "https://www.www.example.com/",
			want: "https://www.example.com/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := Canonical(tt.in)
			if tt.err {
				if err == nil {
					t.Errorf("expected error for %q, got %q", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error for %q: %v", tt.in, err)
				return
			}
			if got != tt.want {
				t.Errorf("Canonical(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}

	// --- Determinism check ---
	for _, raw := range []string{
		"https://ex.com/a?z=1&a=1#frag",
		"HTTP://WWW.EX.COM/Path/",
		"https://ex.com:443/page?id=7&sort=desc",
	} {
		got1, _ := Canonical(raw)
		got2, _ := Canonical(raw)
		if got1 != got2 {
			t.Errorf("Canonical not deterministic for %q: %q vs %q", raw, got1, got2)
		}
	}
}

// -----------------------------------------------------------------------
// ResolveAggregator table test — 4 aggregator shapes + 2 non-aggregators

func TestResolveAggregator(t *testing.T) {
	tests := []struct {
		name              string
		srcName           string
		link, content     string
		commentsURL       string
		wantArticleURL    string
		wantDiscussionURL string // empty means nil
	}{
		// --- Hacker News: article + discussion ---
		{
			name:              "hn_with_comments",
			srcName:           "Hacker News",
			link:              "https://blog.cloudflare.com/outage-postmortem",
			commentsURL:       "https://news.ycombinator.com/item?id=12345",
			wantArticleURL:    "https://blog.cloudflare.com/outage-postmortem",
			wantDiscussionURL: "https://news.ycombinator.com/item?id=12345",
		},
		// --- Hacker News: no comments URL ---
		{
			name:              "hn_no_comments",
			srcName:           "Hacker News",
			link:              "https://example.com/article",
			wantArticleURL:    "https://example.com/article",
		},
		// --- Lobsters: article + discussion ---
		{
			name:              "lobsters_with_comments",
			srcName:           "Lobsters",
			link:              "https://example.org/paper",
			commentsURL:       "https://lobste.rs/s/abc123",
			wantArticleURL:    "https://example.org/paper",
			wantDiscussionURL: "https://lobste.rs/s/abc123",
		},
		// --- Reddit link post: outbound article + discussion ---
		{
			name:              "reddit_link_post",
			srcName:           "r/programming",
			link:              "https://reddit.com/r/programming/comments/xyz/",
			content:           `<p>Check out <a href="https://blog.example.com/post">this post</a> and <a href="https://other.com/noise">noise</a></p>`,
			wantArticleURL:    "https://blog.example.com/post",
			wantDiscussionURL: "https://reddit.com/r/programming/comments/xyz/",
		},
		// --- Reddit self-post: no discussion ---
		{
			name:              "reddit_self_post",
			srcName:           "r/MachineLearning",
			link:              "https://reddit.com/r/MachineLearning/comments/abc/",
			content:           `<p>No links here, just text.</p>`,
			wantArticleURL:    "https://reddit.com/r/MachineLearning/comments/abc/",
		},
		// --- Reddit with only reddit.com links ---
		{
			name:              "reddit_only_reddit_links",
			srcName:           "r/programming",
			link:              "https://reddit.com/r/programming/comments/def/",
			content:           `<p>See <a href="https://old.reddit.com/link">reddit</a></p>`,
			wantArticleURL:    "https://reddit.com/r/programming/comments/def/",
		},
		// --- Techmeme: no discussion ---
		{
			name:              "techmeme",
			srcName:           "Techmeme",
			link:              "https://techcrunch.com/article",
			wantArticleURL:    "https://techcrunch.com/article",
		},
		// --- Non-aggregator: passthrough ---
		{
			name:              "non_aggregator_passthrough",
			srcName:           "The Verge",
			link:              "https://theverge.com/2024/1/1/apple-vision",
			commentsURL:       "https://theverge.com/comments/123",
			wantArticleURL:    "https://theverge.com/2024/1/1/apple-vision",
		},
		// --- Another non-aggregator ---
		{
			name:              "non_aggregator_another",
			srcName:           "Ars Technica",
			link:              "https://arstechnica.com/gadgets/12345",
			wantArticleURL:    "https://arstechnica.com/gadgets/12345",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := feed.ParsedItem{
				Title:       "Test Title",
				Link:        tt.link,
				Content:     tt.content,
				CommentsURL: tt.commentsURL,
			}
			src := config.Source{Name: tt.srcName}

			article, discussion := ResolveAggregator(p, src)

			if article.URL != tt.wantArticleURL || article.Kind != "article" {
				t.Errorf("article = {URL:%q, Kind:%q}, want {URL:%q, Kind:\"article\"}",
					article.URL, article.Kind, tt.wantArticleURL)
			}

			if tt.wantDiscussionURL == "" {
				if discussion != nil {
					t.Errorf("expected no discussion, got URL=%q", discussion.URL)
				}
			} else {
				if discussion == nil {
					t.Errorf("expected discussion URL %q, got nil", tt.wantDiscussionURL)
				} else if discussion.URL != tt.wantDiscussionURL || discussion.Kind != "discussion" {
					t.Errorf("discussion = {URL:%q, Kind:%q}, want {URL:%q, Kind:\"discussion\"}",
						discussion.URL, discussion.Kind, tt.wantDiscussionURL)
				}
			}
		})
	}
}

// -----------------------------------------------------------------------
// Shortener resolver: stubbed redirect + error fallback

func TestResolveShortener(t *testing.T) {
	ctx := context.Background()

	// Stub resolver that follows a single redirect.
	stub := &stubResolver{
		redirects: map[string]string{
			"https://bit.ly/abc123": "https://example.com/target-article",
			"https://t.co/xyz789":  "https://blog.example.org/post",
		},
	}

	tests := []struct {
		name     string
		url      string
		resolver Resolver
		want     string
	}{
		{
			name:     "bitly_redirect",
			url:      "https://bit.ly/abc123",
			resolver: stub,
			want:     "https://example.com/target-article",
		},
		{
			name:     "tco_redirect",
			url:      "https://t.co/xyz789",
			resolver: stub,
			want:     "https://blog.example.org/post",
		},
		{
			name:     "non_shortener_no_resolver_call",
			url:      "https://example.com/article?id=1",
			resolver: stub,
			want:     "https://example.com/article?id=1",
		},
		{
			name:     "nil_resolver_fallback",
			url:      "https://bit.ly/abc123",
			resolver: nil,
			want:     "https://bit.ly/abc123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveShortener(ctx, tt.resolver, tt.url)
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if got != tt.want {
				t.Errorf("ResolveShortener(%q) = %q, want %q", tt.url, got, tt.want)
			}
		})
	}

	// --- Resolver error falls back to canonicalized original URL ---
	errResolver := &stubResolver{err: errStub}
	got, err := ResolveShortener(ctx, errResolver, "https://bit.ly/fail")
	if err != nil {
		t.Errorf("expected no error on resolver failure, got: %v", err)
	}
	if got != "https://bit.ly/fail" {
		t.Errorf("fallback URL = %q, want canonicalized original", got)
	}
}

// -----------------------------------------------------------------------
// Helpers

type stubResolver struct {
	redirects map[string]string
	err       error
}

func (r *stubResolver) Resolve(_ context.Context, u string) (string, error) {
	if r.err != nil {
		return "", r.err
	}
	if target, ok := r.redirects[u]; ok {
		return target, nil
	}
	return "", errStub
}

// assert.AnError is a sentinel used by the stub.
type errString string

func (e errString) Error() string   { return string(e) }
func (e errString) Unwrap() error   { return nil }

var errStub = errString("stub resolver error")
