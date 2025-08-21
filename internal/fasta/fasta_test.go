package fasta

import (
	"strings"
	"testing"
)

func TestParseFastaSimple(t *testing.T) {
	input := ">seq1\nATGC\n>seq2 desc\nGGTT\n"
	recs := ParseFasta(strings.NewReader(input))
	if len(recs) != 2 {
		t.Fatalf("expected 2 records, got %d", len(recs))
	}
	if recs[0].Header != "seq1" || recs[0].Sequence != "ATGC" {
		t.Fatalf("unexpected first record: %+v", recs[0])
	}
	if recs[1].Header != "seq2 desc" || recs[1].Sequence != "GGTT" {
		t.Fatalf("unexpected second record: %+v", recs[1])
	}
}
