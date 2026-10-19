package store

import "time"

// TimeLayout is the one on-disk timestamp format. It matches what SQLite's
// CURRENT_TIMESTAMP and datetime() produce, so every DATETIME column holds
// the same shape and string comparison between a stored value and a bound
// parameter is also a chronological comparison.
const TimeLayout = "2006-01-02 15:04:05"

// FormatTime renders t in TimeLayout, in UTC. Use it for every timestamp
// written to the database and for every timestamp bound as a comparison
// parameter.
func FormatTime(t time.Time) string {
	return t.UTC().Format(TimeLayout)
}

// ParseTime reads a timestamp in TimeLayout or RFC3339 (the format older
// rows were written in before migration 2).
func ParseTime(s string) (time.Time, error) {
	for _, layout := range []string{TimeLayout, time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, &time.ParseError{Layout: TimeLayout, Value: s}
}
