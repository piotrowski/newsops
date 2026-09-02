package feed

import (
	"crypto/sha1"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Write stores the raw body to <CacheDir>/<sha1(url)>/<YYYY-MM-DD>.xml,
// creating directories as needed. Returns the path written.
func Write(cacheDir string, url string, body []byte) (string, error) {
	dir := filepath.Join(cacheDir, fmt.Sprintf("%x", sha1.Sum([]byte(url))))
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}

	name := time.Now().Format("2006-01-02") + ".xml"
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, body, 0644); err != nil {
		return "", err
	}
	return path, nil
}
