package ncbi

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// httpClient performs requests; tests may replace it with a mock transport.
var httpClient = &http.Client{Timeout: 20 * time.Second}

// FetchTranslationFromGenBank fetches the GenBank record (XML) for the given
// nucleotide accession and extracts the first CDS translation found. Returns
// empty string if not found.
func FetchTranslationFromGenBank(accession string) (string, error) {
	if accession == "" {
		return "", nil
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
