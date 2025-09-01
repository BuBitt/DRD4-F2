package translator

// Pacote translator fornece um wrapper fino em torno de ferramentas externas de tradução
// (ex.: seqkit) para traduzir registros FASTA únicos em sequências proteicas.
// O pacote mantém a lógica de tradução fora do TUI e do fluxo principal.

import (
	"context"
	"drd4/internal/fasta"
	"os"
	"os/exec"
	"strings"
	"time"
)

// TranslatorRecord representa um único registro que pode requerer tradução externa.
// Index deve ser o índice na slice original de variants para que os chamadores
// possam mapear os resultados de volta.
type TranslatorRecord struct {
	Index    int
	Header   string
	Sequence string
}

// TranslateMissing utiliza o seqkitPath fornecido para traduzir a sequência
// nucleotídica de cada registro em uma sequência proteica. Retorna um mapa
// do índice do registro para a sequência proteica traduzida. Se seqkitPath
// for vazio, retorna um mapa vazio e sem erro.
//
// Esta função não registra logs; os chamadores devem registrar contagens ou erros conforme desejado.
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
