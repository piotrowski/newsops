package main

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"news-ops/internal/store"
)

// decideFlags hold the subcommand-specific flags.
var decideNote string

// validActions lists all accepted decision actions.
var validActions = []string{"shown", "briefed", "drafted", "posted", "dismissed"}

// decideResult is the JSON output from the decide subcommand.
type decideResult struct {
	Action         string   `json:"action"`
	Applied        []int64  `json:"applied"`
	AlreadyShown   []int64  `json:"already_shown"`
	Unknown        []int64  `json:"unknown"`
}

func cmdDecide(ctx context.Context, args []string) (any, error) {
	if len(args) == 0 {
		return &ExitError{Code: 2, Slug: "invalid_argument", Message: "usage: newsops decide <story_ids> <action> [--note \"...\"]"}, nil
	}

	// Last arg is the action; everything before that is story IDs, given
	// either space-separated or as one comma-separated list.
	action := args[len(args)-1]
	var idStrings []string
	for _, a := range args[:len(args)-1] {
		for _, part := range strings.Split(a, ",") {
			if part = strings.TrimSpace(part); part != "" {
				idStrings = append(idStrings, part)
			}
		}
	}
	if len(idStrings) == 0 {
		return &ExitError{Code: 2, Slug: "invalid_argument", Message: "usage: newsops decide <story_ids> <action> [--note \"...\"]"}, nil
	}

	if !isValidAction(action) {
		return &ExitError{Code: 2, Slug: "invalid_argument", Message: fmt.Sprintf("invalid action %q — valid actions: %s", action, strings.Join(validActions, ", "))}, nil
	}

	// Parse story IDs.
	storyIDs := make([]int64, len(idStrings))
	for i, s := range idStrings {
		id, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return &ExitError{Code: 2, Slug: "invalid_argument", Message: fmt.Sprintf("invalid story ID %q", s)}, nil
		}
		storyIDs[i] = id
	}

	db, err := store.Open(ctx, dbDir, store.ReadWrite)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	// Validate all story IDs exist before writing anything.
	_, unknownIDs, err := validateStoryIDs(ctx, db, storyIDs)
	if err != nil {
		return nil, fmt.Errorf("validate stories: %w", err)
	}

	if len(unknownIDs) > 0 {
		return &ExitError{Code: 2, Slug: "unknown_story", Message: fmt.Sprintf("stories not found: %s", strings.Join(stringInts(unknownIDs), ", ")), Fields: map[string]any{
			"action": action,
			"applied":  []int64{},
			"already_shown": []int64{},
			"unknown": unknownIDs,
		}}, nil
	}

	// All IDs are valid — proceed with the decision.
	var applied, alreadyShown []int64

	err = db.Tx(ctx, func(tx *sql.Tx) error {
		for _, id := range storyIDs {
			if action == "shown" {
				// Check if already shown.
				var shownAt sql.NullString
				err := tx.QueryRowContext(ctx, `SELECT shown_at FROM stories WHERE id=?`, id).Scan(&shownAt)
				if err != nil {
					return fmt.Errorf("check story %d: %w", id, err)
				}
				if shownAt.Valid {
					alreadyShown = append(alreadyShown, id)
					continue
				}
				// Set shown_at only if currently null.
				_, err = tx.ExecContext(ctx, `UPDATE stories SET shown_at=CURRENT_TIMESTAMP WHERE id=? AND shown_at IS NULL`, id)
				if err != nil {
					return fmt.Errorf("update story %d: %w", id, err)
				}
			}

			// Always append a decisions row.
			_, err := tx.ExecContext(ctx,
				`INSERT INTO decisions (story_id, action, note) VALUES (?, ?, ?)`,
				id, action, decideNote)
			if err != nil {
				return fmt.Errorf("insert decision: %w", err)
			}

			applied = append(applied, id)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("decide: %w", err)
	}

	return &decideResult{
		Action:         action,
		Applied:        applied,
		AlreadyShown:   alreadyShown,
		Unknown:        unknownIDs,
	}, nil
}

func isValidAction(action string) bool {
	for _, a := range validActions {
		if a == action {
			return true
		}
	}
	return false
}

// validateStoryIDs checks which story IDs exist and returns (existing, unknown).
func validateStoryIDs(ctx context.Context, db store.DB, storyIDs []int64) ([]int64, []int64, error) {
	if len(storyIDs) == 0 {
		return nil, nil, nil
	}

	var existing, unknown []int64
	for _, id := range storyIDs {
		var count int
		err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM stories WHERE id=?`, id).Scan(&count)
		if err != nil {
			return nil, nil, fmt.Errorf("check story %d: %w", id, err)
		}
		if count == 0 {
			unknown = append(unknown, id)
		} else {
			existing = append(existing, id)
		}
	}

	return existing, unknown, nil
}

func stringInts(ids []int64) []string {
	s := make([]string, len(ids))
	for i, id := range ids {
		s[i] = strconv.FormatInt(id, 10)
	}
	return s
}
