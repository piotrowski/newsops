package dedupe

import (
	"html"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// NormalizeTitle strips the decoration outlets add, leaving comparable text.
//
// Steps (applied in order):
//  1. Decode HTML entities; NFKC-normalize Unicode.
//  2. Strip an outlet suffix: trailing " | X", " - X", " — X", " · X" where X
//     matches a source name (case-insensitive) or is at most four words.
//     Only strip one suffix.
//  3. Strip known prefixes: "Show HN: ", "Ask HN: ", "Tell HN: ", "Launch HN: ",
//     and Reddit tags [P], [R], [D], [N] at the start.
//  4. Lowercase.
//  5. Replace punctuation with spaces; collapse whitespace; trim.
func NormalizeTitle(title string, sourceNames []string) string {
	// Step 1: Decode HTML entities; NFKC-normalize Unicode.
	result := html.UnescapeString(title)
	result, _, _ = transform.String(norm.NFKC, result)

	// Step 2: Strip one outlet suffix.
	result = stripOneSuffix(result, sourceNames)

	// If normalization produced empty or only whitespace, return lowercased original.
	if strings.TrimSpace(result) == "" {
		return strings.ToLower(title)
	}

	// Step 3: Strip known prefixes.
	result = stripPrefixes(result)

	// Step 4: Lowercase.
	result = strings.ToLower(result)

	// Step 5: Replace punctuation/symbols/spaces with single spaces, then trim.
	// Preserve version-like patterns (e.g., "1.90", "2.21") so they stay as one token.
	var b strings.Builder
	b.Grow(len(result))
	for i := 0; i < len(result); {
		r, size := utf8.DecodeRuneInString(result[i:])
		if r == utf8.RuneError {
			i++
			continue
		}
		if unicode.IsPunct(r) || unicode.IsSymbol(r) || unicode.IsSpace(r) {
			// Check if this is a dot within a version pattern (digit.digit).
			if r == '.' && i+size < len(result) {
				nextR, _ := utf8.DecodeRuneInString(result[i+size:])
				prevStart := i
				for prevStart > 0 {
					prevStart--
					pr, _ := utf8.DecodeLastRuneInString(result[:prevStart+1])
					if !unicode.IsDigit(pr) {
						break
					}
				}
				prevLen := i - prevStart
				if prevLen > 0 && unicode.IsDigit(nextR) {
					b.WriteRune('.')
					i += size
					continue
				}
			}
			b.WriteByte(' ')
		} else {
			b.WriteRune(r)
		}
		i += size
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// stripOneSuffix removes a single trailing outlet suffix of the form
// " | X", " - X", " — X", or " · X" where X is either a known source name
// (case-insensitive) or at most four words. Only one suffix is stripped.
func stripOneSuffix(s string, sourceNames []string) string {
	delims := []string{" | ", " - ", " — ", " · "}

	for _, d := range delims {
		idx := strings.LastIndex(s, d)
		if idx < 0 {
			continue
		}

		suffix := strings.TrimSpace(s[idx+len(d):])
		if suffix == "" {
			continue
		}

		// Check if suffix matches a source name (case-insensitive).
		for _, name := range sourceNames {
			if strings.EqualFold(suffix, name) {
				return s[:idx]
			}
		}

		// Check if suffix is at most four words.
		if len(strings.Fields(suffix)) <= 4 {
			return s[:idx]
		}
	}

	return s
}

// stripPrefixes removes known HN/Reddit prefixes from the start of a title.
func stripPrefixes(s string) string {
	prefixes := []string{
		"show hn: ",
		"ask hn: ",
		"tell hn: ",
		"launch hn: ",
	}
	for _, p := range prefixes {
		if strings.HasPrefix(strings.ToLower(s), p) {
			return s[len(p):]
		}
	}

	// Strip leading [P], [R], [D], [N] tags.
	if len(s) > 3 && s[0] == '[' {
		closeBracket := strings.IndexByte(s, ']')
		if closeBracket > 0 && closeBracket < 4 {
			tag := s[1:closeBracket]
			if len(tag) == 1 && unicode.IsLetter(rune(tag[0])) {
				rest := strings.TrimLeft(s[closeBracket+1:], " \t")
				if rest != "" {
					return rest
				}
			}
		}
	}

	return s
}

// ---------------------------------------------------------------------------
// SimHash — word-token fingerprint with FNV-1a hashing

// fnv1aHash hashes a string to 64 bits using FNV-1a.
func fnv1aHash(s string) uint64 {
	const (
		offset = 14695981039346656037
		prime  = 1099511628211
	)
	h := uint64(offset)
	for i := range len(s) {
		_ = i // unused, just iterate over bytes
		h ^= uint64(s[i])
		h *= prime
	}
	return h
}

// SimHash returns a 64-bit fingerprint of the normalized title.
// Word tokens are hashed with FNV-1a, weighted by their length in characters.
// Signed weights accumulate per bit position; the sign bits form the fingerprint.
func SimHash(normalized string) uint64 {
	tokens := strings.Fields(normalized)
	if len(tokens) == 0 {
		return 0
	}

	// Accumulate signed weights for each of the 64 bit positions.
	var weights [64]int32

	for _, token := range tokens {
		h := fnv1aHash(token)
		w := int32(len(token)) // weight = token length in chars
		for i := range 64 {
			if h&(1<<uint(i)) != 0 {
				weights[i] += w
			} else {
				weights[i] -= w
			}
		}
	}

	var fingerprint uint64
	for i := range 64 {
		if weights[i] > 0 {
			fingerprint |= (1 << uint(i))
		}
	}

	return fingerprint
}

// Hamming returns the bit distance between two SimHash fingerprints.
func Hamming(a, b uint64) int {
	x := a ^ b
	count := 0
	for x != 0 {
		count++
		x &= x - 1
	}
	return count
}

// SimilarTitles reports whether two raw titles are the same story, at the
// configured maximum Hamming distance. Titles with fewer than 4 tokens after
// normalization always return false to avoid false merges on short titles.
func SimilarTitles(a, b string, sourceNames []string, maxDist int) bool {
	na := NormalizeTitle(a, sourceNames)
	nb := NormalizeTitle(b, sourceNames)

	// Fewer than 4 tokens: too few to trust similarity.
	if len(strings.Fields(na)) < 4 || len(strings.Fields(nb)) < 4 {
		return false
	}

	dist := Hamming(SimHash(na), SimHash(nb))
	return dist <= maxDist
}
