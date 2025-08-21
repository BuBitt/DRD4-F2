package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type Config struct {
	InputFasta       string `json:"input_fasta"`
	OutputJSON       string `json:"output_json"`
	LogFile          string `json:"log_file"`
	LogLevel         string `json:"log_level"`
	NcbiCachePath    string `json:"ncbi_cache_path"`
	NcbiApiKey       string `json:"ncbi_api_key"`
	NcbiCacheTTLSecs int64  `json:"ncbi_cache_ttl_seconds"`
}

// LoadConfig loads a JSON config from the given path. If path is empty, looks for ./config.json.
func LoadConfig(path string) (*Config, error) {
	if path == "" {
		path = "config.json"
	}
	f, err := os.Open(path)
	if err != nil {
		// not fatal: return defaults
		return &Config{}, nil
	}
	defer f.Close()
	var c Config
	dec := json.NewDecoder(f)
	if err := dec.Decode(&c); err != nil {
		return nil, err
	}
	return &c, nil
}

// ResolveSecret resolves a secret value which can be one of:
// - empty: returns empty
// - "env:VARNAME" -> returns the value of environment variable VARNAME
// - "file:/absolute/or/relative/path" -> returns the file contents (trimmed)
// - otherwise, returns the value as-is
func ResolveSecret(v string) (string, error) {
	if v == "" {
		return "", nil
	}
	if strings.HasPrefix(v, "env:") {
		name := strings.TrimPrefix(v, "env:")
		return os.Getenv(name), nil
	}
	if strings.HasPrefix(v, "file:") {
		p := strings.TrimPrefix(v, "file:")
		b, err := os.ReadFile(p)
		if err != nil {
			return "", fmt.Errorf("failed to read secret file: %w", err)
		}
		return strings.TrimSpace(string(b)), nil
	}
	return v, nil
}
