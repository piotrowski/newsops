package store

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const lockFile = "news.lock"

// LockedError is returned when another process holds the write lock.
type LockedError struct {
	PID       int
	HeldSince time.Time
}

func (e *LockedError) Error() string {
	return fmt.Sprintf("database locked by PID %d since %s", e.PID, e.HeldSince.Format(time.RFC3339))
}

// lockFile returns the full path to the lock file within dir.
func lockPath(dir string) string { return filepath.Join(dir, lockFile) }

// openLock opens (and creates if needed) the lock file in dir.
func openLock(dir string) (*os.File, error) {
	path := lockPath(dir)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, fmt.Errorf("create lock directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	return f, nil
}

// acquireLock takes an exclusive, non-blocking flock on the lock file.
// On success it writes "<pid> <RFC3339>" so the holder is identifiable.
// On contention it reads that line and returns a LockedError.
func acquireLock(f *os.File) error {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Seek(0, 0)
		data := make([]byte, 1024)
		n, _ := f.Read(data)
		return parseLockedError(string(data[:n]))
	}

	// We got it — truncate and write our identity.
	f.Truncate(0)
	fmt.Fprintf(f, "%d %s", os.Getpid(), time.Now().Format(time.RFC3339))
	f.Sync()
	return nil
}

// parseLockedError extracts PID and HeldSince from a lock-file line.
func parseLockedError(line string) error {
	var pid int
	var heldStr string
	n, err := fmt.Sscanf(line, "%d %s", &pid, &heldStr)
	if n >= 2 && err == nil {
		held, perr := time.Parse(time.RFC3339, heldStr)
		if perr == nil {
			return &LockedError{PID: pid, HeldSince: held}
		}
	}
	return &LockedError{PID: -1, HeldSince: time.Now()}
}
