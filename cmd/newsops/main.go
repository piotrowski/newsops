package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"news-ops/internal/store"
)

// Version and Commit are injected at build time via -ldflags.
var (
	Version = "dev"
	Commit  = "none"
)

// Command is the signature every subcommand must implement.
type Command func(ctx context.Context, args []string) (any, error)

// ExitError carries an explicit exit code and machine-readable slug.
type ExitError struct {
	Code    int            `json:"-"`
	Slug    string         `json:"error"`
	Message string         `json:"message"`
	Fields  map[string]any `json:"-"`
}

func (e ExitError) Error() string { return e.Message }

// exitCodeError carries an explicit exit code out of cobra's error path.
type exitCodeError struct{ code int }

func (e exitCodeError) Error() string { return "exit" }
func (e exitCodeError) ExitCode() int { return e.code }

// commands is the routing table. Every subcommand is registered here;
// later tickets replace stub bodies without touching this map.
var commands = map[string]Command{
	"status":        cmdStatus,
	"sources:check": cmdSourcesCheck,
	"fetch":         cmdFetch,
	"dedupe":        cmdDedupe,
	"candidates":    cmdCandidates,
	"collect":       cmdCollect,
	"query":         cmdQuery,
	"decide":        cmdDecide,
	"show":          cmdShow,
	"version":       cmdVersion,
}

// --- global state (filled by cobra flag binding) ---

var (
	jsonOut    bool
	dbDir      string
	configDir  string
	digestPath string
	verbose    bool
	stderr     = os.Stderr

	// fetch / collect flags.
	fetchSource  string
	fetchMaxAge  time.Duration
	fetchSince   string
	fetchNoCache bool

	// collect flags. Separate from fetchMaxAge because the two commands have
	// different defaults and cobra applies a default at registration time.
	collectMaxAge time.Duration

	// candidates / collect flags.
	candidatesSince        string
	candidatesIncludeShown bool
	candidatesLimit        int
)

// commandFlags registers the subcommand-specific flags. Commands absent from
// this table take only the global flags.
var commandFlags = map[string]func(*cobra.Command){
	"sources:check": func(c *cobra.Command) {
		c.Flags().StringVar(&fetchSource, "source", "", "check one source by name")
	},
	"fetch": func(c *cobra.Command) {
		c.Flags().StringVar(&fetchSource, "source", "", "restrict fetch to one source by name")
		c.Flags().DurationVar(&fetchMaxAge, "max-age", 0, "skip if last success is fresher than this duration (e.g. 6h)")
		c.Flags().StringVar(&fetchSince, "since", "", "override fetch window start with RFC3339 time")
		c.Flags().BoolVar(&fetchNoCache, "no-cache", false, "skip raw-body writes during fetch")
	},
	"dedupe": func(c *cobra.Command) {
		c.Flags().DurationVar(&dedupeWindow, "window", 48*time.Hour, "title-match time window")
		c.Flags().IntVar(&dedupeLimit, "limit", 0, "cap how many unassigned items to process (0 = unlimited)")
	},
	"candidates": func(c *cobra.Command) {
		c.Flags().StringVar(&candidatesSince, "since", "", "override window start with RFC3339 time")
		c.Flags().BoolVar(&candidatesIncludeShown, "include-shown", false, "include already-shown stories")
		c.Flags().IntVar(&candidatesLimit, "limit", 0, "override candidate limit (0 = use config default)")
	},
	"collect": func(c *cobra.Command) {
		c.Flags().DurationVar(&collectMaxAge, "max-age", 6*time.Hour, "re-run a stage only if its last success is older than this")
		c.Flags().IntVar(&candidatesLimit, "limit", 0, "override candidate limit (0 = use config default)")
		c.Flags().StringVar(&fetchSource, "source", "", "restrict fetch to one source by name")
		c.Flags().BoolVar(&fetchNoCache, "no-cache", false, "skip raw-body writes during fetch")
	},
	"query": func(c *cobra.Command) {
		c.Flags().StringVar(&queryText, "text", "", "full-text search terms")
		c.Flags().StringVar(&queryDomain, "domain", "", "restrict to a source name or URL host")
		c.Flags().Int64Var(&queryStoryID, "story", 0, "story ID for --related")
		c.Flags().BoolVar(&queryRelated, "related", false, "find stories related to --story")
		c.Flags().IntVar(&queryDays, "days", 90, "look back this many days")
		c.Flags().IntVar(&queryLimit, "limit", 20, "maximum results")
	},
	"decide": func(c *cobra.Command) {
		c.Flags().StringVar(&decideNote, "note", "", "free-text note stored with the decision")
	},
}

// exitCoder is implemented by results that carry their own exit code while
// still being printed as a normal (non-error) payload. collect uses it: the
// JSON is usable but exit 1 signals a partial fetch.
type exitCoder interface{ ExitCode() int }

func main() {
	root := &cobra.Command{
		Use:   "newsops <command>",
		Short: "News-ops CLI — source collection, deduplication, and publishing",
		Long: `newsops is a command-line tool for news operations.

Subcommands:
  status        Show current state of the pipeline
  sources:check Verify source availability
  fetch         Collect articles from configured sources
  dedupe        Remove duplicate candidates
  candidates    List candidate articles for review
  collect       Collect and index articles
  query         Search collected articles
  decide        Decide which candidates to show
  show          Display selected articles
  version       Show version information

Global flags:
  --json            Output machine-readable JSON
  --db <dir>        Database directory (default "data/")
  --config <dir>    Configuration directory (default "config/")
  --digest <path>   Digest file reported by status (default "digest.md")
  --verbose         Print diagnostic messages to stderr`,
		SilenceErrors: true,
		SilenceUsage:  true,
		// Flags but no subcommand: usage, exit 2 (same as no args at all).
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.Usage()
			return exitCodeError{code: 2}
		},
	}

	// Global flags on root command; subcommands inherit them.
	root.PersistentFlags().BoolVar(&jsonOut, "json", false, "machine-readable JSON output")
	root.PersistentFlags().StringVar(&dbDir, "db", "data/", "database directory")
	root.PersistentFlags().StringVar(&configDir, "config", "config/", "configuration directory")
	root.PersistentFlags().StringVar(&digestPath, "digest", "digest.md", "digest file path (reported by status)")
	root.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "verbose output to stderr")

	// Build subcommands from the routing table.
	for name, fn := range commands {
		sub := &cobra.Command{
			Use:   name,
			Short: commandSummary(name),
			RunE: func(cmd *cobra.Command, args []string) error {
				return runCommand(cmd.Context(), fn, args)
			},
		}
		if reg, ok := commandFlags[name]; ok {
			reg(sub)
		}
		root.AddCommand(sub)
	}

	// No subcommand given — print usage.
	if len(os.Args) < 2 {
		root.Usage()
		os.Exit(2)
	}

	if err := root.Execute(); err != nil {
		// Check for our exit-code carrier.
		if e, ok := err.(interface{ ExitCode() int }); ok {
			os.Exit(e.ExitCode())
		}
		slug := "invalid_argument"
		if strings.Contains(err.Error(), "unknown command") {
			slug = "unknown_command"
		}
		envelope := buildEnvelope(slug, err.Error(), nil)
		if jsonOut || hasJSONFlag(os.Args[1:]) {
			fmt.Fprintln(os.Stdout, mustMarshal(envelope))
		} else {
			fmt.Fprintf(stderr, "error: %s — %s\n", slug, err)
		}
		os.Exit(2)
	}
}

// hasJSONFlag reports whether --json appears in args. Flag parsing may have
// aborted before binding jsonOut, so the error path checks the raw args.
func hasJSONFlag(args []string) bool {
	for _, a := range args {
		if a == "--json" || a == "--json=true" {
			return true
		}
	}
	return false
}

// runCommand executes a Command and handles output formatting.
func runCommand(ctx context.Context, fn Command, args []string) error {
	result, err := fn(ctx, args)
	// Stub commands return ExitError as the result value.
	if exitErr, ok := result.(*ExitError); ok && exitErr != nil {
		return handleExitError(exitErr)
	}
	if err != nil {
		var lockErr *store.LockedError
		if errors.As(err, &lockErr) {
			return handleLockedError(lockErr)
		}
		return handleError(err)
	}
	if err := outputJSON(result); err != nil {
		return err
	}
	if ec, ok := result.(exitCoder); ok && ec.ExitCode() != 0 {
		return exitCodeError{code: ec.ExitCode()}
	}
	return nil
}

// handleExitError prints the envelope and returns a wrapped error.
func handleExitError(e *ExitError) error {
	envelope := buildEnvelope(e.Slug, e.Message, e.Fields)
	if jsonOut {
		fmt.Fprintln(os.Stdout, mustMarshal(envelope))
	} else {
		fmt.Fprintf(stderr, "error: %s — %s\n", e.Slug, e.Message)
	}
	return exitCodeError{code: e.Code}
}

// handleLockedError prints the locked envelope and returns an exit code.
func handleLockedError(e *store.LockedError) error {
	fields := map[string]any{
		"pid":        e.PID,
		"held_since": e.HeldSince.Format(time.RFC3339),
	}
	envelope := buildEnvelope("locked", e.Error(), fields)
	if jsonOut {
		fmt.Fprintln(os.Stdout, mustMarshal(envelope))
	} else {
		fmt.Fprintf(stderr, "error: locked — %s\n", e.Error())
	}
	return exitCodeError{code: 2}
}

// handleError formats plain errors into the standard envelope.
func handleError(err error) error {
	exitErr := ExitError{Code: 2, Slug: "internal", Message: err.Error()}
	envelope := buildEnvelope(exitErr.Slug, exitErr.Message, nil)
	if jsonOut {
		fmt.Fprintln(os.Stdout, mustMarshal(envelope))
	} else {
		fmt.Fprintf(stderr, "error: %s — %s\n", exitErr.Slug, exitErr.Message)
	}
	return exitCodeError{code: exitErr.Code}
}

// outputJSON marshals and prints a success result.
func outputJSON(result any) error {
	if jsonOut {
		fmt.Fprintln(os.Stdout, mustMarshal(result))
	} else {
		fmt.Fprintln(os.Stdout, humanize(result))
	}
	return nil
}

// --- helpers ---

func buildEnvelope(slug, msg string, fields map[string]any) map[string]any {
	env := map[string]any{
		"error":   slug,
		"message": msg,
	}
	for k, v := range fields {
		env[k] = v
	}
	return env
}

func mustMarshal(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		fmt.Fprintf(stderr, "marshal error: %v\n", err)
		os.Exit(2)
	}
	return string(b)
}

func commandSummary(name string) string {
	summaries := map[string]string{
		"status":        "Show current state of the pipeline",
		"sources:check": "Verify source availability",
		"fetch":         "Collect articles from configured sources",
		"dedupe":        "Remove duplicate candidates",
		"candidates":    "List candidate articles for review",
		"collect":       "Collect and index articles",
		"query":         "Search collected articles",
		"decide":        "Decide which candidates to show",
		"show":          "Display selected articles",
		"version":       "Show version information",
	}
	if s, ok := summaries[name]; ok {
		return s
	}
	return ""
}

func humanize(v any) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("%+v", v)
	}
	return string(b)
}

// --- stub commands (replaced by later tickets) ---

// cmdVersion is fully implemented in this ticket.
func cmdVersion(_ context.Context, _ []string) (any, error) {
	return map[string]string{
		"version": Version,
		"commit":  Commit,
	}, nil
}
