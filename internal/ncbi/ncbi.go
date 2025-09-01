package ncbi

// Pacote ncbi fornece um pequeno helper para buscar traduções do GenBank
// usando o serviço efetch da NCBI com um cache baseado em arquivo. O pacote
// expõe funções para traduções em lote, controle de cache e recuperação de metadados.

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

// httpClient realiza requisições; testes podem substituí-lo por um transporte mock.
var httpClient *http.Client

func init() {
	tr := &http.Transport{
		MaxIdleConns:        200,
		MaxIdleConnsPerHost: 50,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	httpClient = &http.Client{Transport: tr, Timeout: 30 * time.Second}

	// iniciar rotina em background para persistir o cache
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			saveCache()
		}
	}()
}

// Estruturas do cache
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

// TTL do cache em segundos (padrão 7 dias)
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

// SetCacheFilePath define o caminho do arquivo usado para o cache (para testes ou config).
func SetCacheFilePath(p string) {
	cacheFilePath = p
	// reload on next access
	cacheLoaded = false
}

// SetCacheTTLSeconds define o TTL do cache via string similar a variável de ambiente (para testes/config).
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
	// criar snapshot sob lock e gravar sem segurar o lock
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

// FlushCache força a escrita síncrona do cache em memória para disco.
// Chame isto no encerramento do programa para evitar perda de entradas recentes.
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

// FetchTranslations busca traduções para múltiplos accessions usando uma única chamada efetch
// e retorna um mapa accession->translation. Usa xml.Decoder para ser streaming e robusto.
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
							// ler texto do accession
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
									// nenhum elemento accession encontrado para esta tradução; coletar
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
				// se nenhum mapeamento baseado em accession encontrado mas coletamos traduções
				// e a requisição foi para um único accession, atribuir a primeira tradução coletada
				if len(res) == 0 && len(collected) > 0 && len(missing) == 1 {
					res[missing[0]] = collected[0]
					// no pb info available here
					setCached(missing[0], collected[0], 0, len(collected[0]))
				}
				return res, nil
			}
			if resp.StatusCode == 429 {
				lastErr = fmt.Errorf("ncbi efetch retornou 429")
				// tentar respeitar o header Retry-After
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

// FetchTranslationFromGenBank busca o registro GenBank (XML) para o accession nucleotídico fornecido
// e extrai a primeira tradução CDS encontrada. Retorna string vazia se não encontrada.
// Esta função usa FetchTranslations internamente para buscar um accession.
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

// GetCachedMetadata retorna tradução, pbCount, aaCount e flag encontrado para um accession a partir do cache.
// Não tenta buscar na rede; apenas lê o cache em memória/disco.
func GetCachedMetadata(acc string) (string, int, int, bool) {
	if acc == "" {
		return "", 0, 0, false
	}
	loadCache()
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	if e, ok := cache[acc]; ok {
		return e.Translation, e.PBCount, e.AACount, true
	}
	return "", 0, 0, false
}
