package ncbi

import (
	"io/ioutil"
	"net/http"
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

	got, err := FetchTranslationFromGenBank("FAKE_ACC")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "MKQRST" {
		t.Fatalf("expected MKQRST, got %q", got)
	}
}
