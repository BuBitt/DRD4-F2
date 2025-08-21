package ncbi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// httpClient performs requests; tests may replace it with a mock transport.
var httpClient = &http.Client{Timeout: 20 * time.Second}

// Cache structures
type cachedEntry struct {
	Translation string `json:"translation"`
	RetrievedAt int64  `json:"retrieved_at"`
}

var (
	cacheMu       sync.RWMutex
	cache         map[string]cachedEntry
	cacheLoaded   bool
	cacheFilePath string
)

// cache TTL in seconds (default 7 days)
func cacheTTL() int64 {
	if s := os.Getenv("NCBI_CACHE_TTL_SECONDS"); s != "" {
		if v, err := time.ParseDuration(s + "s"); err == nil {
			return int64(v.Seconds())
		}
	}
	return int64(7 * 24 * 3600)
}

func defaultCachePath() string {
	if cacheFilePath != "" {
		return cacheFilePath
	}
	if dir, err := os.UserCacheDir(); err == nil {
		p := filepath.Join(dir, "drd4")
		_ = os.MkdirAll(p, 0o755)
		return filepath.Join(p, "ncbi_cache.json")
	}
	return filepath.Join(os.TempDir(), "drd4_ncbi_cache.json")
}

func loadCache() {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if cacheLoaded {
		return
	}
	path := defaultCachePath()
	cache = make(map[string]cachedEntry)
	data, err := os.ReadFile(path)
	if err != nil {
		cacheLoaded = true
		return
	}
	_ = json.Unmarshal(data, &cache)
	cacheLoaded = true
}

func saveCache() {
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	path := defaultCachePath()
	b, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(path, b, 0o644)
}

func getCached(acc string) (string, bool) {
	loadCache()
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	e, ok := cache[acc]
	if !ok {
		return "", false
	}
	ttl := cacheTTL()
	if ttl > 0 && time.Now().Unix()-e.RetrievedAt > ttl {
		return "", false
	}
	return e.Translation, true
}

func setCached(acc, tr string) {
	if acc == "" || tr == "" {
		return
	}
	loadCache()
	cacheMu.Lock()
	cache[acc] = cachedEntry{Translation: tr, RetrievedAt: time.Now().Unix()}
	cacheMu.Unlock()
	saveCache()
}

// FetchTranslationFromGenBank fetches the GenBank record (XML) for the given
// nucleotide accession and extracts the first CDS translation found. Returns
// empty string if not found.
func FetchTranslationFromGenBank(accession string) (string, error) {
	if accession == "" {
		return "", nil
	}

	// check cache first
	if v, ok := getCached(accession); ok {
		return v, nil
	}

	base := "https://eutils.ncbi.nlm.nih.gov/entrez/eutils/efetch.fcgi?db=nuccore&id=%s&rettype=gb&retmode=xml"
	apiKey := os.Getenv("NCBI_API_KEY")
	if apiKey != "" {
		base += "&api_key=" + apiKey
	}
	url := fmt.Sprintf(base, accession)

	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("User-Agent", "drd4-fetcher/1.0 (https://example)")
		resp, err := httpClient.Do(req)
		if err != nil {
			lastErr = err
		} else {
			defer resp.Body.Close()
			if resp.StatusCode == 200 {
				data, err := io.ReadAll(resp.Body)
				if err != nil {
					return "", err
				}
				text := string(data)

				// XML-aware naive extraction: look for the qualifier name "translation"
				// then extract the next <GBQualifier_value>...</GBQualifier_value>
				needle := "<GBQualifier_name>translation</GBQualifier_name>"
				i := strings.Index(text, needle)
				if i == -1 {
					return "", nil
				}
				// find the next value tag after the name
				valOpen := "<GBQualifier_value>"
				valIdx := strings.Index(text[i:], valOpen)
				if valIdx == -1 {
					return "", nil
				}
				start := i + valIdx + len(valOpen)
				endTag := "</GBQualifier_value>"
				end := strings.Index(text[start:], endTag)
				if end == -1 {
					return "", nil
				}
				translation := text[start : start+end]
				// Cleanup whitespace/newlines
				translation = strings.ReplaceAll(translation, "\n", "")
				translation = strings.ReplaceAll(translation, " ", "")
				// save to cache (synchronous)
				setCached(accession, translation)
				return translation, nil
			}
			if resp.StatusCode == 429 {
				lastErr = fmt.Errorf("ncbi efetch returned 429")
				time.Sleep(time.Duration(attempt*500) * time.Millisecond)
				continue
			}
			body, _ := io.ReadAll(resp.Body)
			return "", fmt.Errorf("ncbi efetch returned status %d: %s", resp.StatusCode, string(body))
		}
		time.Sleep(time.Duration(attempt*300) * time.Millisecond)
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", nil
}
