package dedupe

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"news-ops/internal/config"
	"news-ops/internal/feed"
)

// Type aliases for cleaner function signatures.
type ParsedItem = feed.ParsedItem
type Source = config.Source
var trackingParams = map[string]bool{
	"utm_source":   true, "utm_medium":  true, "utm_campaign": true,
	"utm_term":     true, "utm_content": true, "ref":         true,
	"ref_src":      true, "source":      true, "fbclid":      true,
	"gclid":        true, "mc_cid":      true, "mc_eid":      true,
	"igshid":       true, "_hsenc":      true, "_hsmi":       true,
	"spm":          true, "at_medium":   true, "at_campaign": true,
}

// Canonical normalizes a URL for equality comparison.
//
// Rules applied in order:
//  1. Parse; reject non-http(s) with an error.
//  2. Lowercase scheme and host; strip leading www.
//  3. Drop fragment entirely.
//  4. Remove tracking query parameters (utm_*, ref, fbclid, etc.).
//  5. Sort remaining query parameters by key.
//  6. Strip trailing slash (except bare "/").
//  7. Drop default ports (:80 on http, :443 on https).
func Canonical(rawURL string) (string, error) {
	parsed, err := url.ParseRequestURI(rawURL)
	if err != nil {
		return "", fmt.Errorf("parse %q: %w", rawURL, err)
	}

	scheme := strings.ToLower(parsed.Scheme)
	switch scheme {
	case "http", "https":
	default:
		return "", fmt.Errorf("unsupported scheme %q in %q", scheme, rawURL)
	}

	host := strings.ToLower(parsed.Hostname())
	if strings.HasPrefix(host, "www.") {
		host = host[4:]
	}
	if parsed.Port() != "" {
		host += ":" + parsed.Port()
	}

	// Drop fragment — ParseRequestURI does not separate fragments,
	// so strip "#..." from RawQuery before processing.
	if idx := strings.Index(parsed.RawQuery, "#"); idx >= 0 {
		parsed.RawQuery = parsed.RawQuery[:idx]
	}
	// When there is no query string, ParseRequestURI embeds the fragment into Path.
	if idx := strings.Index(parsed.Path, "#"); idx >= 0 {
		parsed.Path = parsed.Path[:idx]
	}

	// Remove tracking params and sort remaining ones.
	if len(parsed.RawQuery) > 0 {
		q, _ := url.ParseQuery(parsed.RawQuery)
		filtered := make(url.Values)
		for k, vals := range q {
			if trackingParams[k] || (strings.HasPrefix(k, "utm_") && len(k) > 4) {
				continue
			}
			filtered[k] = vals
		}
		keys := make([]string, 0, len(filtered))
		for k := range filtered {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var parts []string
		for _, k := range keys {
			for _, v := range filtered[k] {
				parts = append(parts, k+"="+v)
			}
		}
		parsed.RawQuery = strings.Join(parts, "&")
	}

	// Strip trailing slash except bare "/".
	if len(parsed.Path) > 1 && strings.HasSuffix(parsed.Path, "/") {
		parsed.Path = parsed.Path[:len(parsed.Path)-1]
	}

	// Drop default ports.
	if (scheme == "http" && parsed.Port() == "80") ||
		(scheme == "https" && parsed.Port() == "443") {
		host = strings.SplitN(host, ":", 2)[0]
	}

	return fmt.Sprintf("%s://%s%s%s", scheme, host, parsed.Path, queryOrEmpty(parsed.RawQuery)), nil
}

// queryOrEmpty returns the query string with a leading "?", or "" if empty.
func queryOrEmpty(raw string) string {
	if raw == "" {
		return ""
	}
	return "?" + raw
}

// Resolver follows one redirect hop for known shorteners. Injected so tests
// never touch the network; pass nil to disable resolution entirely.
type Resolver interface {
	Resolve(ctx context.Context, u string) (string, error)
}

// shortenerHosts lists hosts that are known URL shorteners.
var shortenerHosts = map[string]bool{
	"bit.ly":      true,
	"t.co":        true,
	"buff.ly":     true,
	"lnkd.in":     true,
	"ow.ly":       true,
	"tinyurl.com": true,
	"goo.gl":      true,
	"dlvr.it":     true,
	"trib.al":     true,
}

// ResolveShortener follows one redirect hop for known shortener hosts.
// On any error it falls back to canonicalizing the original URL without error.
func ResolveShortener(ctx context.Context, resolver Resolver, rawURL string) (string, error) {
	parsed, err := url.ParseRequestURI(rawURL)
	if err != nil {
		return Canonical(rawURL) // best-effort fallback
	}

	host := strings.ToLower(parsed.Hostname())
	if strings.HasPrefix(host, "www.") {
		host = host[4:]
	}

	if !shortenerHosts[host] {
		return Canonical(rawURL)
	}

	if resolver == nil {
		return Canonical(rawURL)
	}

	resolved, err := resolver.Resolve(ctx, rawURL)
	if err != nil {
		// Failed resolution: fall back to canonicalized short URL.
		return Canonical(rawURL)
	}
	return Canonical(resolved)
}

// Link represents a resolved link from an aggregator item.
type Link struct {
	URL  string
	Kind string // "article" | "discussion"
}

// Aggregator kinds recognised by ResolveAggregator.
const (
	aggNone     = ""
	aggHN       = "hn"
	aggLobsters = "lobsters"
	aggReddit   = "reddit"
	aggTechmeme = "techmeme"
)

// knownAggregators maps legacy display names to an aggregator kind. Detection
// by feed host (aggregatorKind) is preferred because configured source names
// are free-form ("hacker-news-digest", "techmeme").
var knownAggregators = map[string]string{
	"Hacker News":       aggHN,
	"Lobsters":          aggLobsters,
	"r/programming":     aggReddit,
	"r/MachineLearning": aggReddit,
	"Techmeme":          aggTechmeme,
}

// aggregatorHosts maps a feed host (or a parent domain of it) to a kind.
var aggregatorHosts = map[string]string{
	"news.ycombinator.com": aggHN,
	"hnrss.org":            aggHN,
	"lobste.rs":            aggLobsters,
	"reddit.com":           aggReddit,
	"techmeme.com":         aggTechmeme,
}

// aggregatorKind classifies a source by its feed URL host, then by its
// (free-form) configured name.
func aggregatorKind(src Source) string {
	if u, err := url.Parse(src.URL); err == nil {
		host := strings.ToLower(u.Hostname())
		for h, kind := range aggregatorHosts {
			if isSubdomainOrSame(host, h) {
				return kind
			}
		}
	}
	if kind, ok := knownAggregators[src.Name]; ok {
		return kind
	}
	name := strings.ToLower(src.Name)
	switch {
	case strings.Contains(name, "hacker news"), strings.Contains(name, "hacker-news"),
		strings.Contains(name, "hackernews"), strings.Contains(name, "hnrss"):
		return aggHN
	case strings.Contains(name, "lobste"):
		return aggLobsters
	case strings.Contains(name, "reddit"), strings.HasPrefix(name, "r/"):
		return aggReddit
	case strings.Contains(name, "techmeme"):
		return aggTechmeme
	}
	return aggNone
}

// ResolveAggregator returns the article link and, when the source is a known
// aggregator, the discussion link. For a non-aggregator, discussion is nil
// and article is the item's own link.
func ResolveAggregator(p ParsedItem, src Source) (article Link, discussion *Link) {
	switch aggregatorKind(src) {
	case aggHN, aggLobsters:
		// Article is the item link (the original URL embedded in the feed),
		// discussion is the comments thread.
		if p.CommentsURL != "" {
			return Link{URL: p.Link, Kind: "article"}, &Link{URL: p.CommentsURL, Kind: "discussion"}
		}
		return Link{URL: p.Link, Kind: "article"}, nil

	case aggReddit:
		// Reddit: try to find the first outbound link in content whose host
		// differs from reddit.com. If found, that's the article; the item
		// link is the discussion thread.
		outbound := findOutboundLink(p.Content, "reddit.com")
		if outbound != "" {
			return Link{URL: outbound, Kind: "article"}, &Link{URL: p.Link, Kind: "discussion"}
		}
		// Self-post or no outbound link: treat as a normal item.
		return Link{URL: p.Link, Kind: "article"}, nil

	case aggTechmeme:
		// Techmeme already carries the source article URL in p.Link.
		return Link{URL: p.Link, Kind: "article"}, nil

	default:
		return Link{URL: p.Link, Kind: "article"}, nil
	}
}

// findOutboundLink returns the first href in HTML content whose host differs
// from the excludedHost. Returns "" when no such link exists.
func findOutboundLink(content, excludedHost string) string {
	const tagPrefix = `<a href="`
	const tagPrefixSingle = `<a href='`

	for len(content) > 0 {
		var idx int
		if i := strings.Index(content, tagPrefix); i >= 0 {
			idx = i
		} else if i := strings.Index(content, tagPrefixSingle); i >= 0 {
			idx = i
		} else {
			return ""
		}

		content = content[idx+len(tagPrefix):] // skip past the opening quote
		end := strings.IndexByte(content, '"')
		if end < 0 {
			return ""
		}

		href := content[:end]
		parsed, err := url.ParseRequestURI(href)
		if err != nil {
			content = content[end+1:]
			continue
		}

		host := strings.ToLower(parsed.Hostname())
		if host != "" && !isSubdomainOrSame(host, excludedHost) {
			return href
		}

		content = content[end+1:]
	}
	return ""
}
// isSubdomainOrSame reports whether host is excludedHost or a subdomain of it.
func isSubdomainOrSame(host, parent string) bool {
	if host == parent {
		return true
	}
	return strings.HasSuffix(host, "."+parent)
}
