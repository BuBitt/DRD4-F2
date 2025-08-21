package translator

// Package translator provides a thin wrapper around external translation
// tools (e.g., seqkit) to translate single FASTA records to protein
// sequences. The package keeps the translation logic outside the TUI and
// main control flow.

import (
	"context"
	"drd4/internal/fasta"
	"os"
	"os/exec"
	"strings"
	"time"
)

// TranslatorRecord represents a single record that may require external
// translation. Index should be the index in the original variants slice so
// callers can map results back.
type TranslatorRecord struct {
	Index    int
	Header   string
	Sequence string
}

// TranslateMissing uses the provided seqkitPath to translate each record's
// nucleotide sequence into a protein sequence. It returns a map from record
// Index to translated protein sequence. If seqkitPath is empty, it returns an
// empty map and no error.
//
// This function does not log; callers should log counts or errors as desired.
func TranslateMissing(records []TranslatorRecord, seqkitPath string, perRecordTimeout time.Duration) (map[int]string, error) {
	res := make(map[int]string)
	if seqkitPath == "" {
		return res, nil
	}
	if perRecordTimeout <= 0 {
		perRecordTimeout = 15 * time.Second
	}

	for _, r := range records {
		// create temp fasta file for this record
		tf, err := os.CreateTemp("", "variant-*.fasta")
		if err != nil {
			// skip this record on error
			continue
		}
		_, _ = tf.WriteString(">" + r.Header + "\n" + r.Sequence + "\n")
		fname := tf.Name()
		tf.Close()

		ctx, cancel := context.WithTimeout(context.Background(), perRecordTimeout)
		cmd := exec.CommandContext(ctx, seqkitPath, "translate", "-w", "0", fname)
		out, err := cmd.CombinedOutput()
		cancel()
		_ = os.Remove(fname)
		if err != nil {
			// skip on error
			continue
		}
		prots := fasta.ParseFasta(strings.NewReader(string(out)))
		if len(prots) > 0 {
			res[r.Index] = prots[0].Sequence
		}
	}

	return res, nil
}
