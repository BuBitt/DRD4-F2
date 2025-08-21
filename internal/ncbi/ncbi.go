package ncbi

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// httpClient performs requests; tests may replace it with a mock transport.
var httpClient *http.Client

func init() {
	tr := &http.Transport{
		MaxIdleConns:        200,
		MaxIdleConnsPerHost: 50,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	httpClient = &http.Client{Transport: tr, Timeout: 30 * time.Second}

	// start background cache flusher
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			saveCache()
		}
	}()
}

// Cache structures
type cachedEntry struct {
	Translation string `json:"translation"`
	RetrievedAt int64  `json:"retrieved_at"`
	PBCount     int    `json:"pb_count,omitempty"`
	AACount     int    `json:"aa_count,omitempty"`
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

// SetCacheFilePath sets the file path used for the cache (for testing or config).
func SetCacheFilePath(p string) {
	cacheFilePath = p
	// reload on next access
	cacheLoaded = false
}

// SetCacheTTLSeconds sets the cache TTL via environment-like string (for tests/config).
func SetCacheTTLSeconds(ttl int64) {
	// set via env is supported in cacheTTL(); as a simple override we mutate NCBI_CACHE_TTL_SECONDS env
	os.Setenv("NCBI_CACHE_TTL_SECONDS", fmt.Sprintf("%d", ttl))
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
	// snapshot under lock and write without holding lock
	cacheMu.RLock()
	snap := make(map[string]cachedEntry, len(cache))
	for k, v := range cache {
		snap[k] = v
	}
	cacheMu.RUnlock()

	path := defaultCachePath()
	b, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(path, b, 0o644)
}

// FlushCache forces a synchronous write of the in-memory cache to disk.
// Call this on program shutdown to avoid losing recent cache entries.
func FlushCache() {
	saveCache()
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

func setCached(acc, tr string, pbCount, aaCount int) {
	if acc == "" || tr == "" {
		return
	}
	loadCache()
	cacheMu.Lock()
	cache[acc] = cachedEntry{Translation: tr, RetrievedAt: time.Now().Unix(), PBCount: pbCount, AACount: aaCount}
	cacheMu.Unlock()
	// do not block; background goroutine flushes periodically
}

// FetchTranslations fetches translations for multiple accessions using a single efetch call
// and returns a map accession->translation. It uses xml.Decoder so it's streaming and robust.
func FetchTranslations(ctx context.Context, accessions []string) (map[string]string, error) {
	res := make(map[string]string)
	if len(accessions) == 0 {
		return res, nil
	}
	// check cache first and collect missing
	var missing []string
	for _, a := range accessions {
		if v, ok := getCached(a); ok {
			res[a] = v
		} else {
			missing = append(missing, a)
		}
	}
	if len(missing) == 0 {
		return res, nil
	}

	base := "https://eutils.ncbi.nlm.nih.gov/entrez/eutils/efetch.fcgi?db=nuccore&rettype=gb&retmode=xml"
	apiKey := os.Getenv("NCBI_API_KEY")
	if apiKey != "" {
		base += "&api_key=" + apiKey
	}

	// batch all missing in one call (NCBI supports comma-separated ids)
	ids := strings.Join(missing, ",")
	url := fmt.Sprintf(base+"&id=%s", ids)

	var lastErr error
	for attempt := 1; attempt <= 4; attempt++ {
		// respect context
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return res, err
		}
		req.Header.Set("User-Agent", "drd4-fetcher/1.0 (https://example)")
		resp, err := httpClient.Do(req)
		if err != nil {
			lastErr = err
		} else {
			defer resp.Body.Close()
			if resp.StatusCode == 200 {
				// stream-parse XML
				dec := xml.NewDecoder(resp.Body)
				var currentAcc string
				var lastQualifierName string
				var collected []string
				var currentPBLen int
				for {
					tk, err := dec.Token()
					if err != nil {
						if err == io.EOF {
							break
						}
						lastErr = err
						break
					}
					switch el := tk.(type) {
					case xml.StartElement:
						if el.Name.Local == "GBSeq_accession-version" || el.Name.Local == "GBSeq_locus" || el.Name.Local == "GBSeq_primary-accession" {
							// read accession text
							var accText string
							_ = dec.DecodeElement(&accText, &el)
							currentAcc = strings.TrimSpace(accText)
						} else if el.Name.Local == "GBQualifier_name" {
							var name string
							_ = dec.DecodeElement(&name, &el)
							lastQualifierName = strings.TrimSpace(name)
						} else if el.Name.Local == "GBQualifier_value" {
							var val string
							_ = dec.DecodeElement(&val, &el)
							if lastQualifierName == "translation" {
								tr := strings.ReplaceAll(val, "\n", "")
								tr = strings.ReplaceAll(tr, " ", "")
								if currentAcc != "" {
									res[currentAcc] = tr
									setCached(currentAcc, tr, currentPBLen, len(tr))
								} else {
									// no accession element found for this translation; collect it
									collected = append(collected, tr)
								}
							}
						} else if el.Name.Local == "GBSeq_length" {
							// nucleotide length
							var lstr string
							_ = dec.DecodeElement(&lstr, &el)
							// parse int; ignore errors
							if lstr != "" {
								if v, err := strconv.Atoi(strings.TrimSpace(lstr)); err == nil {
									currentPBLen = v
								}
							}
						}
					}
				}
				// if no accession-based mappings found but we have collected translations
				// and the request was for a single accession, assign the first collected translation
				if len(res) == 0 && len(collected) > 0 && len(missing) == 1 {
					res[missing[0]] = collected[0]
					// no pb info available here
					setCached(missing[0], collected[0], 0, len(collected[0]))
				}
				return res, nil
			}
			if resp.StatusCode == 429 {
				lastErr = fmt.Errorf("ncbi efetch returned 429")
				// try to respect Retry-After header
				if ra := resp.Header.Get("Retry-After"); ra != "" {
					if s, err := time.ParseDuration(ra + "s"); err == nil {
						time.Sleep(s)
					}
				}
				// exponential backoff with jitter
				backoff := time.Duration((1<<attempt))*200*time.Millisecond + time.Duration(rand.Intn(300))*time.Millisecond
				time.Sleep(backoff)
				continue
			}
			body, _ := io.ReadAll(resp.Body)
			return res, fmt.Errorf("ncbi efetch returned status %d: %s", resp.StatusCode, string(body))
		}
		// transient network error -> backoff with jitter
		time.Sleep(time.Duration(attempt*200)*time.Millisecond + time.Duration(rand.Intn(200))*time.Millisecond)
	}
	if lastErr != nil {
		return res, lastErr
	}
	return res, nil
}

// FetchTranslationFromGenBank fetches the GenBank record (XML) for the given
// nucleotide accession and extracts the first CDS translation found. Returns
// empty string if not found.
// FetchTranslationFromGenBank fetches a single accession translation using FetchTranslations under the hood.
func FetchTranslationFromGenBank(accession string) (string, error) {
	if accession == "" {
		return "", nil
	}
	// check cache first
	if v, ok := getCached(accession); ok {
		return v, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	m, err := FetchTranslations(ctx, []string{accession})
	if err != nil {
		return "", err
	}
	if t, ok := m[accession]; ok {
		return t, nil
	}
	return "", nil
}
