package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const NL2SQLRequestTimeout = 6 * time.Minute

func SchemaPath(root string) string {
	path := Setting("NL2SQL_SCHEMA_CACHE", filepath.Join("NL2SQL", "schema_cache", "schema.json"))
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(root, path)
}

func ResourceRoot() (string, error) {
	if root := os.Getenv("REPORT_REPO_ROOT"); root != "" {
		return filepath.Abs(root)
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "graph.py")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("repository root not found; set REPORT_REPO_ROOT")
		}
		dir = parent
	}
}

func Setting(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func Endpoint(key, fallback string) (string, error) {
	s := Setting(key, fallback)
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("%s must be an absolute HTTP URL", key)
	}
	return s, nil
}

func PositiveInt(key string, fallback int) (int, error) {
	text := Setting(key, strconv.Itoa(fallback))
	value, err := strconv.Atoi(text)
	if err != nil || value < 1 || value > 1_000_000 {
		return 0, fmt.Errorf("%s must be between 1 and 1000000", key)
	}
	return value, nil
}

func Enabled(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
