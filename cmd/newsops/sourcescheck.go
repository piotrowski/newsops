package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"news-ops/internal/config"
	"news-ops/internal/feed"
)

// checkResult is the per-source outcome of a sources:check.
type checkResult struct {
	Name      string `json:"name"`
	URL       string `json:"url"`
	Status    string `json:"status"`
	HTTPStatus int   `json:"http_status"`
	Items     int    `json:"items"`
	Error     string `json:"error,omitempty"`
}

// sourcesCheckResult is the JSON output for sources:check.
type sourcesCheckResult struct {
	Total   int              `json:"total"`
	Passed  int              `json:"passed"`
	Failed  int              `json:"failed"`
	Results []checkResult    `json:"results"`
}

// checkSource performs a single unconditional GET + parse on a source.
func checkSource(ctx context.Context, src config.Source) checkResult {
	r := checkResult{Name: src.Name, URL: src.URL}

	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, "GET", src.URL, nil)
	if err != nil {
		r.Status = "network_error"
		r.Error = fmt.Sprintf("build request: %v", err)
		return r
	}
	req.Header.Set("User-Agent", "newsops/"+Version+" (+personal feed reader)")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		r.Status = "network_error"
		r.Error = fmt.Sprintf("fetch %s: %v", src.URL, err)
		return r
	}
	defer resp.Body.Close()

	r.HTTPStatus = resp.StatusCode

	// Non-2xx responses are http_error regardless of body content.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		r.Status = "http_error"
		r.Error = fmt.Sprintf("http %d", resp.StatusCode)
		return r
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		r.Status = "network_error"
		r.Error = fmt.Sprintf("read %s: %v", src.URL, err)
		return r
	}

	items, parseErr := feed.Parse(body, src)
	if parseErr != nil {
		r.Status = "parse_error"
		r.Error = fmt.Sprintf("parse %s: %v", src.URL, parseErr)
		return r
	}
	r.Items = len(items)

	if r.Items == 0 {
		r.Status = "empty"
		r.Error = "parsed 0 items"
		return r
	}

	r.Status = "ok"
	return r
}

// checkSources performs a concurrent batch check of all sources.
func checkSources(ctx context.Context, srcs []config.Source) []checkResult {
	results := make([]checkResult, len(srcs))
	g := make(chan struct{}, 10)
	var wg sync.WaitGroup
	for i, src := range srcs {
		i, src := i, src
		wg.Add(1)
		go func() {
			defer wg.Done()
			g <- struct{}{}
			results[i] = checkSource(ctx, src)
			<-g
		}()
	}
	wg.Wait()
	return results
}

// cmdSourcesCheck implements the sources:check subcommand.
func cmdSourcesCheck(ctx context.Context, args []string) (any, error) {
	cfg, err := config.Load(configDir)
	if err != nil {
		return &ExitError{Code: 2, Slug: "config_error", Message: fmt.Sprintf("load config: %v", err)}, nil
	}

	sources := cfg.Sources
	if fetchSource != "" {
		var filtered []config.Source
		for _, s := range sources {
			if strings.EqualFold(s.Name, fetchSource) {
				filtered = append(filtered, s)
			}
		}
		if len(filtered) == 0 {
			return &ExitError{Code: 2, Slug: "invalid_argument", Message: fmt.Sprintf("source %q not found in config", fetchSource)}, nil
		}
		sources = filtered
	}

	results := checkSources(ctx, sources)

	passed, failed := 0, 0
	for _, r := range results {
		if r.Status == "ok" {
			passed++
		} else {
			failed++
		}
	}

	exitCode := 0
	if failed > 0 {
		exitCode = 1
	}

	if failed > 0 {
		if !jsonOut {
			printHumanCheck(results, passed, failed)
		}

		return &ExitError{Code: exitCode, Slug: "check_failures", Message: fmt.Sprintf("%d/%d sources failed", failed, len(results)), Fields: map[string]any{
			"total":   len(results),
			"passed":  passed,
			"failed":  failed,
			"results": results,
		}}, nil
	}

	if !jsonOut {
		printHumanCheck(results, passed, failed)
	}

	return &sourcesCheckResult{
		Total:   len(results),
		Passed:  passed,
		Failed:  failed,
		Results: results,
	}, nil
}

func printHumanCheck(results []checkResult, passed, failed int) {
	const maxName = 28
	for _, r := range results {
		name := r.Name
		if len(name) > maxName {
			name = name[:maxName]
		}

		var statusStr string
		switch r.Status {
		case "ok":
			statusStr = "PASS"
		default:
			statusStr = "FAIL"
		}

		var detail string
		if r.HTTPStatus != 0 {
			detail = fmt.Sprintf("%3d", r.HTTPStatus)
		} else {
			detail = "—"
		}

		var itemStr string
		if r.Items > 0 {
			itemStr = fmt.Sprintf("%2d items", r.Items)
		} else {
			itemStr = "—"
		}

		line := fmt.Sprintf("%s  %-*s  %s   %s", statusStr, maxName, name, detail, itemStr)
		if r.Error != "" && (r.Status == "empty" || r.Status == "network_error") {
			line += fmt.Sprintf("   (%s)", r.Error)
		}

		fmt.Fprintln(os.Stdout, line)
	}

	fmt.Fprintf(os.Stdout, "\n%d/%d passed\n", passed, len(results))
}
