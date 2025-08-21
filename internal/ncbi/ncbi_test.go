package ncbi

import (
	"context"
	"io/ioutil"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestFetchTranslationFromGenBank_XML(t *testing.T) {
	// canned XML with a GBQualifier translation
	xml := `<?xml version="1.0"?>
<GBSet>
<GBSeq>
<GBSeq_feature-table>
<GBFeature>
<GBFeature_quals>
<GBQualifier>
<GBQualifier_name>translation</GBQualifier_name>
<GBQualifier_value>MKQRST</GBQualifier_value>
</GBQualifier>
</GBFeature_quals>
</GBFeature>
</GBSeq_feature-table>
</GBSeq>
</GBSet>`

	httpClient = &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Body:       ioutil.NopCloser(strings.NewReader(xml)),
			Header:     make(http.Header),
		}, nil
	})}
	// use a temp cache file so test is hermetic
	tmp := t.TempDir()
	cacheFilePath = filepath.Join(tmp, "ncbi_cache.json")
	// ensure cache is reset
	cache = nil
	cacheLoaded = false

	got, err := FetchTranslationFromGenBank("FAKE_ACC")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "MKQRST" {
		t.Fatalf("expected MKQRST, got %q", got)
	}

	// second call should hit cache and not invoke HTTP transport; replace transport to fail if called
	httpClient = &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		t.Fatalf("HTTP should not be called on cached fetch")
		return nil, nil
	})}

	got2, err := FetchTranslationFromGenBank("FAKE_ACC")
	if err != nil {
		t.Fatalf("unexpected error on cached fetch: %v", err)
	}
	if got2 != "MKQRST" {
		t.Fatalf("expected MKQRST from cache, got %q", got2)
	}
}

// Test fetch translations for multiple accessions returned in a single XML payload.
func TestFetchTranslations_BatchMapping(t *testing.T) {
	xml := `<?xml version="1.0"?>
<GBSet>
<GBSeq>
<GBSeq_primary-accession>ACC1</GBSeq_primary-accession>
<GBSeq_feature-table>
<GBFeature>
<GBFeature_quals>
<GBQualifier>
<GBQualifier_name>translation</GBQualifier_name>
<GBQualifier_value>AAA</GBQualifier_value>
</GBQualifier>
</GBFeature_quals>
</GBFeature>
</GBSeq_feature-table>
</GBSeq>
<GBSeq>
<GBSeq_primary-accession>ACC2</GBSeq_primary-accession>
<GBSeq_feature-table>
<GBFeature>
<GBFeature_quals>
<GBQualifier>
<GBQualifier_name>translation</GBQualifier_name>
<GBQualifier_value>BBB</GBQualifier_value>
</GBQualifier>
</GBFeature_quals>
</GBFeature>
</GBSeq_feature-table>
</GBSeq>
</GBSet>`

	httpClient = &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Body:       ioutil.NopCloser(strings.NewReader(xml)),
			Header:     make(http.Header),
		}, nil
	})}
	tmp := t.TempDir()
	cacheFilePath = filepath.Join(tmp, "ncbi_cache.json")
	cache = nil
	cacheLoaded = false

	ctx := context.Background()
	got, err := FetchTranslations(ctx, []string{"ACC1", "ACC2"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v, ok := got["ACC1"]; !ok || v != "AAA" {
		t.Fatalf("expected ACC1->AAA, got %v", got)
	}
	if v, ok := got["ACC2"]; !ok || v != "BBB" {
		t.Fatalf("expected ACC2->BBB, got %v", got)
	}
}

// Test that FetchTranslations retries on 429 and honors Retry-After header.
func TestFetchTranslations_RetryAndRetryAfter(t *testing.T) {
	calls := 0
	xml := `<?xml version="1.0"?>
<GBSet>
<GBSeq>
<GBSeq_primary-accession>RACC</GBSeq_primary-accession>
<GBSeq_feature-table>
<GBFeature>
<GBFeature_quals>
<GBQualifier>
<GBQualifier_name>translation</GBQualifier_name>
<GBQualifier_value>RRR</GBQualifier_value>
</GBQualifier>
</GBFeature_quals>
</GBFeature>
</GBSeq_feature-table>
</GBSeq>
</GBSet>`

	httpClient = &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			// first call: 429 with Retry-After: 1
			h := make(http.Header)
			h.Set("Retry-After", "1")
			return &http.Response{StatusCode: 429, Body: ioutil.NopCloser(strings.NewReader("")), Header: h}, nil
		}
		return &http.Response{StatusCode: 200, Body: ioutil.NopCloser(strings.NewReader(xml)), Header: make(http.Header)}, nil
	})}

	tmp := t.TempDir()
	cacheFilePath = filepath.Join(tmp, "ncbi_cache.json")
	cache = nil
	cacheLoaded = false

	ctx := context.Background()
	start := time.Now()
	got, err := FetchTranslations(ctx, []string{"RACC"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v, ok := got["RACC"]; !ok || v != "RRR" {
		t.Fatalf("expected RACC->RRR, got %v", got)
	}
	if time.Since(start) < time.Second {
		t.Fatalf("expected at least 1s wait due to Retry-After, elapsed %v", time.Since(start))
	}
}

// Test cache TTL logic: expired entries should not be returned.
func TestCacheTTL_Expiry(t *testing.T) {
	tmp := t.TempDir()
	cacheFilePath = filepath.Join(tmp, "ncbi_cache.json")
	cache = make(map[string]cachedEntry)
	// set an old retrieved_at
	cache["OLDACC"] = cachedEntry{Translation: "OLD", RetrievedAt: time.Now().Unix() - 100000}
	cacheLoaded = true
	SetCacheTTLSeconds(1) // 1 second TTL

	if v, ok := getCached("OLDACC"); ok || v != "" {
		t.Fatalf("expected OLDACC to be expired and not returned, got %v (ok=%v)", v, ok)
	}
}
