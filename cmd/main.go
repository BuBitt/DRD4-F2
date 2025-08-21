package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"drd4/internal/fasta"
	"drd4/internal/ncbi"
)

// Define aqui o cabeçalho da sequência de referência que será usada para o merge.
// Ajuste este valor para corresponder ao cabeçalho exato presente no seu FASTA.
const ReferenceHeader = "NM_000797.4 Homo sapiens dopamine receptor D4 (DRD4), mRNA"

func main() {
	filename := "drd4-tdah.fasta"
	data, err := os.ReadFile(filename)
	if err != nil {
		panic(err)
	}
	content := string(data)

	if filepath.Ext(filename) == ".fasta" {
		records := fasta.ParseFasta(strings.NewReader(content))

		// Try to run MAFFT on the original FASTA file to get aligned sequences.
		alignMap := make(map[string]string)
		if _, err := exec.LookPath("mafft"); err == nil {
			cmd := exec.Command("mafft", "--auto", filename)
			out, err := cmd.Output()
			if err != nil {
				fmt.Fprintln(os.Stderr, "mafft failed:", err)
			} else {
				aligned := fasta.ParseFasta(strings.NewReader(string(out)))
				for _, a := range aligned {
					alignMap[a.Header] = a.Sequence
				}
			}
		} else {
			fmt.Fprintln(os.Stderr, "mafft not found in PATH; nucleotides_align field will contain unaligned sequence")
		}

		type Variant struct {
			Name             string `json:"name"`
			VariantCode      string `json:"variant_code"`
			Nucleotides      string `json:"nucleotides"`
			Translated       string `json:"translated,omitempty"`
			NucleotidesAlign string `json:"nucleotides_align"`
		}
		var variants []Variant

		// write aligned FASTA to a temporary file
		tmp, tmpErr := os.CreateTemp("", "aligned-*.fasta")
		if tmpErr == nil {
			for _, rec := range records {
				seq := rec.Sequence
				if a, ok := alignMap[rec.Header]; ok && a != "" {
					seq = a
				}
				fmt.Fprintf(tmp, ">%s\n%s\n", rec.Header, seq)
			}
			tmp.Close()
			defer os.Remove(tmp.Name())
		} else {
			fmt.Fprintln(os.Stderr, "warning: cannot create temp file for aligned FASTA:", tmpErr)
		}

		// translate using external tool: prefer transeq (EMBOSS), then seqkit
		protMap := make(map[string]string)
		if tmpErr == nil {
			if path, err := exec.LookPath("transeq"); err == nil {
				cmd := exec.Command(path, "-sequence", tmp.Name(), "-outseq", "-")
				out, err := cmd.Output()
				if err != nil {
					fmt.Fprintln(os.Stderr, "transeq failed:", err)
				} else {
					prots := fasta.ParseFasta(strings.NewReader(string(out)))
					for _, p := range prots {
						protMap[p.Header] = p.Sequence
					}
				}
			} else if path, err := exec.LookPath("seqkit"); err == nil {
				cmd := exec.Command(path, "translate", "-w", "0", tmp.Name())
				out, err := cmd.Output()
				if err != nil {
					fmt.Fprintln(os.Stderr, "seqkit translate failed:", err)
				} else {
					prots := fasta.ParseFasta(strings.NewReader(string(out)))
					for _, p := range prots {
						protMap[p.Header] = p.Sequence
					}
				}
			} else {
				fmt.Fprintln(os.Stderr, "no external translator found (transeq or seqkit); translated field will be omitted")
			}
		}

		for _, record := range records {
			alignSeq := record.Sequence
			if a, ok := alignMap[record.Header]; ok && a != "" {
				alignSeq = a
			}

			// extract accession (first token) as variant code
			var acc string
			fields := strings.Fields(record.Header)
			if len(fields) > 0 {
				acc = fields[0]
			}

			translation := ""
			// Prefer translation from GenBank (NCBI) using the accession (first token of header)
			if acc != "" {
				if gb, err := ncbi.FetchTranslationFromGenBank(acc); err != nil {
					fmt.Fprintln(os.Stderr, "ncbi fetch error for", acc, ":", err)
				} else if gb != "" {
					translation = gb
				}
			}
			// Fallback: use translation from external translator if GenBank not available
			if translation == "" {
				if t, ok := protMap[record.Header]; ok {
					translation = t
				}
			}

			variants = append(variants, Variant{
				Name:             record.Header,
				VariantCode:      acc,
				Nucleotides:      record.Sequence,
				Translated:       translation,
				NucleotidesAlign: alignSeq,
			})
		}
		jsonData, err := json.MarshalIndent(variants, "", "  ")
		if err != nil {
			panic(err)
		}
		fmt.Println(string(jsonData))
	} else {
		fmt.Println(content)
	}
}
