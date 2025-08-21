package config

import (
	"encoding/json"
	"os"
)

type Config struct {
	InputFasta            string `json:"input_fasta"`
	OutputJSON            string `json:"output_json"`
	LogFile               string `json:"log_file"`
	LogLevel              string `json:"log_level"`
	NcbiCachePath         string `json:"ncbi_cache_path"`
	NcbiApiKey            string `json:"ncbi_api_key"`
	NcbiCacheTTLSecs      int64  `json:"ncbi_cache_ttl_seconds"`
	UseExternalTranslator bool   `json:"use_external_translator"`
}

// LoadConfig loads a JSON config from the given path. If path is empty, looks for ./config.json.
// In config-only mode, secrets must be provided as literal values in config.json.
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
