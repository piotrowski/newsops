package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Source represents a news feed source.
type Source struct {
	Name string `yaml:"name"`
	Tier int    `yaml:"tier"`
	URL  string `yaml:"url"`
}

// Prefilter holds tuning knobs for the prefilter stage.
type Prefilter struct {
	CandidateLimit    int      `yaml:"candidate_limit"`
	PerSourceCap      int      `yaml:"per_source_cap"`
	TitleSimhashMax   int      `yaml:"title_simhash_max"`
	MaxBackfillDays   int      `yaml:"max_backfill_days"`
	ExcludeTerms      []string `yaml:"exclude_terms"`
}
func toInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int64:
		return n, true
	case float64:
		return int64(n), true
	default:
		return 0, false
	}
}

// DefaultPrefilter returns the default prefilter values.
func DefaultPrefilter() Prefilter {
	return Prefilter{
		CandidateLimit:  40,
		PerSourceCap:    3,
		TitleSimhashMax: 3,
		MaxBackfillDays: 30,
		ExcludeTerms:    []string{},
	}
}

// Config is the complete configuration loaded from disk.
type Config struct {
	Sources        []Source
	PrimaryDomains map[string]bool
	Prefilter      Prefilter
}

// Load reads and validates configuration from dir.
//
// Required files: sources.yml, domains.yml.
// Optional file: prefilter.yml (missing → all defaults; partial → merged with defaults).
func Load(dir string) (*Config, error) {
	// --- sources.yml (required) ---
	srcBytes, err := os.ReadFile(filepath.Join(dir, "sources.yml"))
	if err != nil {
		return nil, fmt.Errorf("read sources: %w", err)
	}

	var srcList struct {
		Sources []Source `yaml:"sources"`
	}
	if err := yaml.Unmarshal(srcBytes, &srcList); err != nil {
		return nil, fmt.Errorf("parse sources: %w", err)
	}
	if err := validateSources(srcList.Sources); err != nil {
		return nil, err
	}

	// --- domains.yml (required) ---
	domBytes, err := os.ReadFile(filepath.Join(dir, "domains.yml"))
	if err != nil {
		return nil, fmt.Errorf("read domains: %w", err)
	}

	var domList struct {
		PrimaryDomains []string `yaml:"primary_domains"`
	}
	if err := yaml.Unmarshal(domBytes, &domList); err != nil {
		return nil, fmt.Errorf("parse domains: %w", err)
	}
	domSet := make(map[string]bool, len(domList.PrimaryDomains))
	for _, d := range domList.PrimaryDomains {
		domSet[d] = true
	}

	// --- prefilter.yml (optional) ---
	pf := DefaultPrefilter()
	pfPath := filepath.Join(dir, "prefilter.yml")
	pfBytes, err := os.ReadFile(pfPath)
	if err == nil {
		var raw map[string]any
		if err := yaml.Unmarshal(pfBytes, &raw); err != nil {
			return nil, fmt.Errorf("parse prefilter: %w", err)
		}
		if v, ok := raw["candidate_limit"]; ok {
			n, ok := toInt64(v)
			if !ok || n <= 0 {
				return nil, fmt.Errorf("prefilter: candidate_limit must be > 0")
			}
			pf.CandidateLimit = int(n)
		}
		if v, ok := raw["per_source_cap"]; ok {
			n, ok := toInt64(v)
			if !ok || n <= 0 {
				return nil, fmt.Errorf("prefilter: per_source_cap must be > 0")
			}
			pf.PerSourceCap = int(n)
		}
		if v, ok := raw["title_simhash_max"]; ok {
			n, ok := toInt64(v)
			if !ok || n < 0 || n > 64 {
				return nil, fmt.Errorf("prefilter: title_simhash_max must be 0..64")
			}
			pf.TitleSimhashMax = int(n)
		}
		if v, ok := raw["max_backfill_days"]; ok {
			n, ok := toInt64(v)
			if !ok || n <= 0 {
				return nil, fmt.Errorf("prefilter: max_backfill_days must be > 0")
			}
			pf.MaxBackfillDays = int(n)
		}
		if v, ok := raw["exclude_terms"]; ok {
			arr, ok := v.([]any)
			if !ok {
				return nil, fmt.Errorf("prefilter: exclude_terms must be a list")
			}
			pf.ExcludeTerms = make([]string, len(arr))
			for i, item := range arr {
				s, ok := item.(string)
				if !ok {
					return nil, fmt.Errorf("prefilter: exclude_terms items must be strings")
				}
				pf.ExcludeTerms[i] = s
			}
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read prefilter: %w", err)
	}

	return &Config{
		Sources:        srcList.Sources,
		PrimaryDomains: domSet,
		Prefilter:      pf,
	}, nil
}

// validateSources checks name uniqueness/non-empty, tier range, and http/https URL.
func validateSources(sources []Source) error {
	seen := make(map[string]bool, len(sources))
	for _, s := range sources {
		if s.Name == "" {
			return fmt.Errorf("source: empty name")
		}
		if seen[s.Name] {
			return fmt.Errorf("source: duplicate name %q", s.Name)
		}
		seen[s.Name] = true

		if s.Tier < 1 || s.Tier > 3 {
			return fmt.Errorf("source %q: tier must be 1..3, got %d", s.Name, s.Tier)
		}

		u, err := url.Parse(s.URL)
		if err != nil {
			return fmt.Errorf("source %q: invalid URL %q: %w", s.Name, s.URL, err)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return fmt.Errorf("source %q: URL scheme must be http or https, got %q", s.Name, u.Scheme)
		}
		if u.Host == "" {
			return fmt.Errorf("source %q: URL missing host: %q", s.Name, s.URL)
		}
	}
	return nil
}

// IsPrimary reports whether rawURL belongs to a configured primary domain.
// It strips "www." from the host before checking.
func (c *Config) IsPrimary(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if strings.HasPrefix(host, "www.") {
		host = host[4:]
	}
	return c.PrimaryDomains[host]
}

// --- helpers for test fixtures ---

// MustLoad is Load panicking on error; useful in tests.
func MustLoad(dir string) *Config {
	c, err := Load(dir)
	if err != nil {
		panic(err)
	}
	return c
}
