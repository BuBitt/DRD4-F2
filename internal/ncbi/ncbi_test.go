package ncbi

import (
	"io/ioutil"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
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
