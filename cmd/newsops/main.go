package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"news-ops/internal/store"
	"github.com/spf13/cobra"
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
	Code    int              `json:"-"`
	Slug    string           `json:"error"`
	Message string           `json:"message"`
	Fields  map[string]any   `json:"-"`
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

// --- global state (filled by persistent pre-run) ---

var (
	jsonOut   bool
	dbDir     string
	configDir string
	verbose   bool
	stderr    = os.Stderr

	// fetch-specific flags (filled by cobra).
	fetchSource   string
	fetchMaxAge   time.Duration
	fetchSince    string
	fetchNoCache       bool

	// candidates-specific flags (filled by cobra).
	candidatesSince        string
	candidatesIncludeShown bool
	candidatesLimit        int
)


func main() {
	root := &cobra.Command{
		Use:           "newsops <command>",
		Short:         "News-ops CLI — source collection, deduplication, and publishing",
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
  --verbose         Print diagnostic messages to stderr`,
		SilenceErrors: true,
		SilenceUsage:  true,
	}

	// Global flags on root command.
	root.PersistentFlags().BoolVar(&jsonOut, "json", false, "machine-readable JSON output")
	root.PersistentFlags().StringVar(&dbDir, "db", "data/", "database directory")
	root.PersistentFlags().StringVar(&configDir, "config", "config/", "configuration directory")
	root.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "verbose output to stderr")
	root.PersistentFlags().StringVar(&fetchSource, "source", "", "restrict fetch to one source by name")
	root.PersistentFlags().DurationVar(&fetchMaxAge, "max-age", 0, "skip if last success is fresher than this duration (e.g. 6h)")
	root.PersistentFlags().StringVar(&fetchSince, "since", "", "override fetch window start with RFC3339 time")
	root.PersistentFlags().BoolVar(&fetchNoCache, "no-cache", false, "skip raw-body writes during fetch")
	root.PersistentFlags().StringVar(&candidatesSince, "candidates-since", "", "override window start with RFC3339 time")
	root.PersistentFlags().BoolVar(&candidatesIncludeShown, "include-shown", false, "include already-shown stories")
	root.PersistentFlags().IntVar(&candidatesLimit, "limit", 0, "override candidate limit (0 = use config default)")

	// Build subcommands from the routing table.
	for name, fn := range commands {
		nameParts := splitCommand(name)
		sub := &cobra.Command{
			Use:   nameParts[0] + spaceOrEmpty(nameParts[1]),
			Short: commandSummary(name),
			RunE: func(cmd *cobra.Command, args []string) error {
				return runCommand(root.Context(), fn, args)
			},
		}
		// Every subcommand inherits global flags.
		sub.PersistentFlags().BoolVar(&jsonOut, "json", false, "")
		sub.PersistentFlags().StringVar(&dbDir, "db", "data/", "")
		sub.PersistentFlags().StringVar(&configDir, "config", "config/", "")
		sub.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "")
		root.AddCommand(sub)
	}
	// unknown subcommands.
	if err := root.ParseFlags(os.Args[1:]); err != nil {
		fmt.Fprintln(stderr, "flag error:", err)
		os.Exit(2)
	}

	// No subcommand given — print usage before cobra runs.
	if len(os.Args) < 2 {
		root.Usage()
		os.Exit(2)
	}

	if err := root.Execute(); err != nil {
		// Check for our exit-code carrier.
		if e, ok := err.(interface{ ExitCode() int }); ok {
			os.Exit(e.ExitCode())
		}
		// Cobra's "unknown command" → our envelope.
		if strings.Contains(err.Error(), "unknown command") {
			envelope := buildEnvelope("unknown_command", fmt.Sprintf("Unknown command %q", os.Args[1]), nil)
			if jsonOut {
				fmt.Fprintln(os.Stdout, mustMarshal(envelope))
			} else {
				fmt.Fprintf(stderr, "error: unknown_command — %s\n", err)
			}
			os.Exit(2)
		}
		os.Exit(2)
	}
}

// runCommand executes a Command and handles output formatting.
func runCommand(ctx context.Context, fn Command, args []string) error {
	result, err := fn(ctx, args)
	// Stub commands return ExitError as the result value.
	if exitErr, ok := result.(*ExitError); ok && exitErr != nil {
		return handleExitError(exitErr)
	}
	if err != nil {
		if lockErr, ok := err.(*store.LockedError); ok {
			return handleLockedError(lockErr)
		}
		return handleError(err)
	}
	return outputJSON(result)
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

// splitCommand splits "foo:bar" into ["foo", "bar"].
// Single-name commands like "status" return ["status", ""].
func splitCommand(name string) []string {
	for i, c := range name {
		if c == ':' {
			return []string{name[:i], name[i+1:]}
		}
	}
	return []string{name, ""}
}

func spaceOrEmpty(s string) string {
	if s == "" {
		return ""
	}
	return " " + s
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
