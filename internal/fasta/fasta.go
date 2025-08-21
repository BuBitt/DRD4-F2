package fasta

import (
	"bufio"
	"io"
	"strings"
)

type FastaRecord struct {
	Header   string
	Sequence string
}

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
