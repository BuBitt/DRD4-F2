package fasta

// Package fasta contains minimal helpers to parse FASTA formatted data used
// by the project. It intentionally keeps parsing simple and conservative.

import (
	"bufio"
	"io"
	"strings"
)

// FastaRecord represents a single FASTA record (header and sequence).
type FastaRecord struct {
	Header   string
	Sequence string
}

// ParseFasta reads FASTA records from r and returns a slice of FastaRecord.
// Lines beginning with '>' denote headers; sequence lines are concatenated.
func ParseFasta(r io.Reader) []FastaRecord {
	scanner := bufio.NewScanner(r)
	var records []FastaRecord
	var current FastaRecord
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, ">") {
			if current.Header != "" {
				records = append(records, current)
			}
			current = FastaRecord{Header: line[1:], Sequence: ""}
		} else {
			current.Sequence += line
		}
	}
	if current.Header != "" {
		records = append(records, current)
	}
	return records
}
