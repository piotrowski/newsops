package dedupe

import (
	"strings"
	"testing"
)

// sourceNames for testing — mirrors real sources.yml names.
var testSourceNames = []string{
	"Hacker News", "Ars Technica", "The Verge", "TechCrunch",
	"Lobsters", "Reuters", "BBC News", "CNN",
	"Wired", "The Register", "ZDNet", "VentureBeat",
}

func TestNormalizeTitle(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		source string // source name for suffix stripping
		want   string
	}{
		// --- Outlet suffix stripping ---
		{
			name:   "cloudflare_ars_suffix",
			in:     "Cloudflare publishes outage postmortem | Ars Technica",
			source: "Ars Technica",
			want:   "cloudflare publishes outage postmortem",
		},
		{
			name:   "apple_verge_dash_suffix",
			in:     "New Apple Vision Pro features - The Verge",
			source: "The Verge",
			want:   "new apple vision pro features",
		},
		{
			name:   "rust_techcrunch_dot_suffix",
			in:     "Rust 1.90 release notes · TechCrunch",
			source: "TechCrunch",
			want:   "rust 1.90 release notes", // dot preserved in version
		},
		// --- Only one suffix stripped, not two ---
		{
			name:   "two_suffixes_one_stripped",
			in:     "Breaking News | BBC News — The Verge",
			source: "BBC News",
			want:   "breaking news bbc news", // strips last suffix only; "|" and em-dash → spaces
		},
		// --- Suffix that is not a source name but ≤4 words ---
		{
			name:   "non_source_suffix_few_words",
			in:     "AI breakthrough in healthcare | Tech Review",
			source: "",
			want:   "ai breakthrough in healthcare",
		},
		// --- Suffix >4 words is NOT stripped ---
		{
			name:   "suffix_too_many_words",
			in:     "New model released by OpenAI — A Deep Dive into the Future of AI",
			source: "",
			want:   "new model released by openai a deep dive into the future of ai",
		},
		// --- HN prefixes ---
		{
			name: "show_hn_prefix",
			in:   "Show HN: A tool for X",
			want: "a tool for x",
		},
		{
			name: "ask_hn_prefix",
			in:   "Ask HN: What is the best database?",
			want: "what is the best database", // question mark → space
		},
		{
			name: "tell_hn_prefix",
			in:   "Tell HN: I built a static site generator",
			want: "i built a static site generator",
		},
		{
			name: "launch_hn_prefix",
			in:   "Launch HN: A new platform for developers",
			want: "a new platform for developers",
		},
		// --- Reddit tags ---
		{
			name: "reddit_tag_p",
			in:   "[P] New product announcement from Google",
			want: "new product announcement from google",
		},
		{
			name: "reddit_tag_r",
			in:   "[R] Review of the latest iPhone",
			want: "review of the latest iphone",
		},
		// --- Lowercase + punctuation → spaces ---
		{
			name: "exclamation_punctuation",
			in:   "OpenAI Ships New Model!",
			want: "openai ships new model",
		},
		{
			name: "version_number_dots",
			in:   "Go 1.22 Released — What's New?",
			want: "go 1.22 released", // em-dash suffix stripped (≤4 words); dot preserved in version
		},
		// --- NFKC normalization (fullwidth → halfwidth) ---
		{
			name: "nfc_fullwidth",
			in:   "Ｓｅｃｕｒｉｔｙ Ｂｕｇ Ｆｏｕｎｄ",
			want: "security bug found", // NFKC → halfwidth, then lowercase
		},
		// --- Title that is only an outlet suffix → returns lowercased original ---
		{
			name:   "suffix_only_title",
			in:     "Ars Technica",
			source: "Ars Technica",
			want:   "ars technica",
		},
		// --- No change when no decoration present ---
		{
			name: "no_decoration",
			in:   "Simple title without decoration",
			want: "simple title without decoration",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sources := testSourceNames
			if tt.source != "" {
				sources = []string{tt.source}
			}
			got := NormalizeTitle(tt.in, sources)
			if got != tt.want {
				t.Errorf("NormalizeTitle(%q, %v) = %q, want %q",
					tt.in, sources, got, tt.want)
			}
		})
	}
}

func TestHamming(t *testing.T) {
	// Self-distance is always 0.
	for _, v := range []uint64{0, 1, ^uint64(0), 0xDEADBEEF, 0xCAFEBABE} {
		if got := Hamming(v, v); got != 0 {
			t.Errorf("Hamming(%#x, %#x) = %d, want 0", v, v, got)
		}
	}

	// All-bits-different.
	if got := Hamming(0, ^uint64(0)); got != 64 {
		t.Errorf("Hamming(0, ^uint64(0)) = %d, want 64", got)
	}

	// Single bit difference.
	if got := Hamming(1, 3); got != 1 {
		t.Errorf("Hamming(1, 3) = %d, want 1", got)
	}
}

func TestSimHashDeterminism(t *testing.T) {
	a := SimHash("test string for determinism check")
	b := SimHash("test string for determinism check")
	if a != b {
		t.Errorf("SimHash not deterministic: %016x vs %016x", a, b)
	}
}

func TestSimilarTitles(t *testing.T) {
	tests := []struct {
		name    string
		a, b    string
		match   bool
		maxDist int
	}{
		// === Must match (distance ≤ 3) ===

		// Acceptance: Cloudflare postmortem — suffix stripped + word order diff
		{
			name:  "cloudflare_postmortem",
			a:     "Cloudflare publishes outage postmortem | Ars Technica",
			b:     "Cloudflare publishes postmortem on outage",
			match: true,
		},

		// Acceptance: Show HN prefix stripped — exactly 4 tokens each
		{
			name:  "show_hn_prefix",
			a:     "Show HN: A tool for X",
			b:     "A tool for X",
			match: true,
		},

		// Acceptance: case-insensitive match
		{
			name:  "case_insensitive",
			a:     "OpenAI Ships New Model",
			b:     "OpenAI ships new model",
			match: true,
		},

		// === Must NOT match ===

		// Acceptance: version bump — caught by 4-token minimum (3 tokens each)
		{
			name:  "postgres_version_bump",
			a:     "Postgres 18 released",
			b:     "Postgres 17 released",
			match: false,
		},

		// Acceptance: different regions — hamming should be high enough
		{
			name:  "aws_region_spain_vs_japan",
			a:     "AWS launches new region in Spain",
			b:     "AWS launches new region in Japan",
			match: false,
		},

		// Acceptance: different languages — caught by 4-token minimum (3 tokens each)
		{
			name:  "rust_vs_go_version",
			a:     "Rust 1.90 announced",
			b:     "Go 1.90 announced",
			match: false,
		},

		// Short title pair — both < 4 tokens
		{
			name:  "short_titles_both_under_four",
			a:     "AI breakthrough",
			b:     "New AI model",
			match: false,
		},

		// One short, one long — short guard triggers
		{
			name:  "one_short_one_long",
			a:     "Bug found",
			b:     "Critical security vulnerability discovered in popular library",
			match: false,
		},

		// Different products entirely
		{
			name:  "different_products",
			a:     "Apple releases new MacBook Pro with M4 chip",
			b:     "Samsung unveils Galaxy S25 with improved camera",
			match: false,
		},

		// Same topic, different details
		{
			name:  "same_topic_different_details",
			a:     "Google announces new search algorithm update for better results",
			b:     "Microsoft updates Bing with new AI-powered features for users",
			match: false,
		},

		// Different years
		{
			name:  "different_years",
			a:     "Apple iPhone 16 sales exceed expectations in first quarter",
			b:     "Apple iPhone 15 sales exceed expectations in second quarter",
			match: false,
		},

		// Different quantities
		{
			name:  "different_quantities",
			a:     "Company reports 50 percent revenue growth in annual earnings report",
			b:     "Company reports 30 percent revenue decline in quarterly earnings report",
			match: false,
		},

		// Different locations
		{
			name:  "different_locations",
			a:     "Major earthquake strikes Turkey causing widespread damage to infrastructure",
			b:     "Powerful earthquake hits Japan triggering tsunami warnings along coast",
			match: false,
		},

		// Very similar but different entity (should still fail)
		{
			name:  "similar_different_entity",
			a:     "Tesla stock surges after record delivery numbers reported by company",
			b:     "Rivian stock surges after record delivery numbers reported by company",
			match: false,
		},

		// Same words, different meaning (negation)
		{
			name:  "negation_difference",
			a:     "Scientists confirm that climate change is not caused by natural cycles",
			b:     "Scientists confirm that climate change is driven by natural cycles",
			match: false,
		},

		// Acceptance: any pair where either < 4 tokens
		{
			name:  "one_word_each",
			a:     "Hello",
			b:     "World",
			match: false,
		},

		// Identical titles — should match (distance 0)
		{
			name:  "identical_titles",
			a:     "Cloudflare publishes outage postmortem | Ars Technica",
			b:     "Cloudflare publishes outage postmortem | Ars Technica",
			match: true,
		},

		// Suffix only difference
		{
			name:  "suffix_only_difference",
			a:     "New Linux kernel release with major performance improvements | The Register",
			b:     "New Linux kernel release with major performance improvements — Ars Technica",
			match: true,
		},

		// HTML entities decode to same punctuation (both lose the "&" token)
		{
			name:  "html_entities",
			a:     "Apple &amp; Google partner on new standard today",
			b:     "Apple & Google partner on new standard today",
			match: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SimilarTitles(tt.a, tt.b, testSourceNames, 3)
			if got != tt.match {
				na := NormalizeTitle(tt.a, testSourceNames)
				nb := NormalizeTitle(tt.b, testSourceNames)
				t.Errorf("SimilarTitles(%q, %q) = %v, want %v\n  normalized: %q vs %q\n  tokens: %d vs %d",
					tt.a, tt.b, got, tt.match, na, nb, len(strings.Fields(na)), len(strings.Fields(nb)))
			}
		})
	}
}
