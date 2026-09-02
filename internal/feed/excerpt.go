package feed

import (
	"crypto/sha256"
	"fmt"
	"strings"

	"golang.org/x/net/html"
)

// Excerpt builds the ~1500-char string the agent skims.
func Excerpt(p ParsedItem) string {
	text := stripHTML(p.Content)
	text = html.UnescapeString(text)
	text = collapseWhitespace(text)

	var parts []string
	parts = append(parts, p.Title)
	if len(p.Categories) > 0 {
		parts = append(parts, strings.Join(p.Categories, ", "))
	}
	parts = append(parts, text)
	full := strings.Join(parts, "\n")

	return truncateAtWord(full, 1500)
}

// BodyHash is the dedupe/change-detection hash of the normalized content.
func BodyHash(p ParsedItem) string {
	text := stripHTML(p.Content)
	text = html.UnescapeString(text)
	text = collapseWhitespace(text)
	h := sha256.New()
	fmt.Fprint(h, text)
	return fmt.Sprintf("%x", h.Sum(nil))
}

// stripHTML parses the HTML and extracts only text content,
// dropping <script>, <style>, and all other markup.
func stripHTML(htmlStr string) string {
	if htmlStr == "" {
		return ""
	}

	doc, err := html.Parse(strings.NewReader(htmlStr))
	if err != nil {
		return simpleStripHTML(htmlStr)
	}

	var buf strings.Builder
	extractText(doc, &buf)
	return buf.String()
}

func extractText(n *html.Node, buf *strings.Builder) {
	if n.Type == html.TextNode {
		buf.WriteString(n.Data)
		return
	}

	// Skip script and style elements entirely.
	if n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style") {
		return
	}

	for c := n.FirstChild; c != nil; c = c.NextSibling {
		extractText(c, buf)
	}
}

// simpleStripHTML is a fallback for malformed HTML: drops anything between < and >.
func simpleStripHTML(s string) string {
	var buf strings.Builder
	inTag := false
	for _, r := range s {
		switch r {
		case '<':
			inTag = true
		case '>':
			inTag = false
		default:
			if !inTag {
				buf.WriteRune(r)
			}
		}
	}
	return buf.String()
}

// collapseWhitespace replaces all runs of whitespace with a single space and trims.
func collapseWhitespace(s string) string {
	var buf strings.Builder
	prevSpace := true // trim leading whitespace
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			if !prevSpace {
				buf.WriteByte(' ')
			}
			prevSpace = true
		} else {
			buf.WriteRune(r)
			prevSpace = false
		}
	}
	return strings.TrimRight(buf.String(), " ")
}

// truncateAtWord truncates s to maxLen characters at a word boundary,
// appending "…" when truncated. Never splits a multi-byte rune.
func truncateAtWord(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}

	truncated := s[:maxLen]
	lastSpace := strings.LastIndexByte(truncated, ' ')
	if lastSpace > 0 {
		return truncated[:lastSpace] + "…"
	}

	// No space found — truncate at byte boundary (spaceless content).
	return truncated + "…"
}
