package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad_validationErrors(t *testing.T) {
	tests := []struct {
		name   string
		dir    string
		wantSub string
	}{
		{"duplicate_name", "duplicate_name", `source: duplicate name "hacker-news"`},
		{"tier_zero", "tier_zero", `tier must be 1..3, got 0`},
		{"tier_four", "tier_four", `tier must be 1..3, got 4`},
		{"bad_url", "bad_url", "invalid URL"},
		{"ftp_url", "ftp_url", `URL scheme must be http or https, got "ftp"`},
		{"candidate_limit_zero", "candidate_limit_zero", `prefilter: candidate_limit must be > 0`},
		{"title_simhash_max_65", "title_simhash_max_65", `prefilter: title_simhash_max must be 0..64`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := filepath.Join("testdata", tt.dir)
			_, err := Load(dir)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Errorf("error %q does not contain %q", err, tt.wantSub)
			}
		})
	}
}

func TestLoad_realConfig(t *testing.T) {
	c, err := Load("../../config")
	if err != nil {
		t.Fatalf("Load(../../config): %v", err)
	}
	if got := len(c.Sources); got != 51 {
		t.Errorf("Sources: got %d, want 51", got)
	}
	if c.PrimaryDomains == nil {
		t.Fatal("PrimaryDomains is nil")
	}
	if len(c.PrimaryDomains) == 0 {
		t.Fatal("PrimaryDomains is empty")
	}
}

func TestLoad_missingPrefilter(t *testing.T) {
	dir := t.TempDir()
	// Write sources and domains, but no prefilter.
	if err := os.WriteFile(filepath.Join(dir, "sources.yml"), []byte(`sources:
  - name: test
    tier: 1
    url: https://example.com/feed
`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "domains.yml"), []byte("primary_domains:\n  - example.com\n"), 0644); err != nil {
		t.Fatal(err)
	}

	c, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	pf := c.Prefilter
	if pf.CandidateLimit != 40 {
		t.Errorf("CandidateLimit: got %d, want 40", pf.CandidateLimit)
	}
	if pf.PerSourceCap != 3 {
		t.Errorf("PerSourceCap: got %d, want 3", pf.PerSourceCap)
	}
	if pf.TitleSimhashMax != 3 {
		t.Errorf("TitleSimhashMax: got %d, want 3", pf.TitleSimhashMax)
	}
	if pf.MaxBackfillDays != 30 {
		t.Errorf("MaxBackfillDays: got %d, want 30", pf.MaxBackfillDays)
	}
	if len(pf.ExcludeTerms) != 0 {
		t.Errorf("ExcludeTerms: got %v, want []", pf.ExcludeTerms)
	}
}

func TestLoad_partialPrefilter(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "sources.yml"), []byte(`sources:
  - name: test
    tier: 1
    url: https://example.com/feed
`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "domains.yml"), []byte("primary_domains:\n  - example.com\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prefilter.yml"), []byte(`candidate_limit: 20
per_source_cap: 5
`), 0644); err != nil {
		t.Fatal(err)
	}

	c, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	pf := c.Prefilter
	if pf.CandidateLimit != 20 {
		t.Errorf("CandidateLimit: got %d, want 20", pf.CandidateLimit)
	}
	if pf.PerSourceCap != 5 {
		t.Errorf("PerSourceCap: got %d, want 5", pf.PerSourceCap)
	}
	// Unspecified fields should use defaults.
	if pf.TitleSimhashMax != 3 {
		t.Errorf("TitleSimhashMax: got %d, want 3 (default)", pf.TitleSimhashMax)
	}
	if pf.MaxBackfillDays != 30 {
		t.Errorf("MaxBackfillDays: got %d, want 30 (default)", pf.MaxBackfillDays)
	}
}

func TestIsPrimary(t *testing.T) {
	c := &Config{
		PrimaryDomains: map[string]bool{
			"arxiv.org": true,
			"github.blog": true,
		},
	}

	tests := []struct {
		url  string
		want bool
	}{
		{"https://arxiv.org/abs/2401.12345", true},
		{"http://www.arxiv.org/abs/2401.12345", true}, // www. stripped
		{"https://github.blog/post/1", true},
		{"https://techcrunch.com/2024/01/01/news/", false},
		{"https://www.techcrunch.com/2024/01/01/news/", false},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			got := c.IsPrimary(tt.url)
			if got != tt.want {
				t.Errorf("IsPrimary(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}
