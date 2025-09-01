package config

import (
	"encoding/json"
	"os"
)

// Config holds runtime configuration loaded from a JSON file.
// Fields map to keys expected in `config.json`.
type Config struct {
	Aligner               string `json:"aligner"`
	InputFasta            string `json:"input_fasta"`
	OutputJSON            string `json:"output_json"`
	LogFile               string `json:"log_file"`
	LogLevel              string `json:"log_level"`
	NcbiCachePath         string `json:"ncbi_cache_path"`
	NcbiApiKey            string `json:"ncbi_api_key"`
	NcbiCacheTTLSecs      int64  `json:"ncbi_cache_ttl_seconds"`
	NcbiConcurrency       int    `json:"ncbi_concurrency"`
	NcbiQPS               int    `json:"ncbi_qps"`
	NcbiBatchSize         int    `json:"ncbi_batch_size"`
	UseExternalTranslator bool   `json:"use_external_translator"`
}

// LoadConfig loads configuration from the provided JSON file path.
// If path is empty, it will try to read `./config.json`.
// When the file does not exist, LoadConfig returns a Config with zero values.
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
