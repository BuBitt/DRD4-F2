package fasta

// Pacote fasta contém helpers mínimos para analisar dados formatados em FASTA
// usados pelo projeto. Intencionalmente mantém o parser simples e conservador.

import (
	"bufio"
	"io"
	"strings"
)

// FastaRecord representa um único registro FASTA (cabeçalho e sequência).
type FastaRecord struct {
	Header   string
	Sequence string
}

// ParseFasta lê registros FASTA de r e retorna uma slice de FastaRecord.
// Linhas iniciadas com '>' denotam cabeçalhos; linhas de sequência são concatenadas.
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
