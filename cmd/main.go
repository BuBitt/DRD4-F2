package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/log"

	"drd4/internal/config"
	"drd4/internal/fasta"
	"drd4/internal/ncbi"
)

// Define aqui o cabeçalho da sequência de referência que será usada para o merge.
// Ajuste este valor para corresponder ao cabeçalho exato presente no seu FASTA.
const ReferenceHeader = "NM_000797.4 Homo sapiens dopamine receptor D4 (DRD4), mRNA"

// timestampWriter prefixes each flushed line with an RFC3339 timestamp.
type timestampWriter struct {
	w   io.Writer
	buf bytes.Buffer
	mu  sync.Mutex
}

// Write buffers bytes until a newline is found; for each full line, write a timestamped line
// to the underlying writer. Partial lines are kept in the buffer.
func (t *timestampWriter) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	n, _ := t.buf.Write(p)
	total := n
	for {
		line, err := t.buf.ReadString('\n')
		if err != nil {
			break
		}
		ts := time.Now().Format(time.RFC3339)
		if _, err := t.w.Write([]byte(ts + " " + line)); err != nil {
			return total, err
		}
	}
	return total, nil
}

func main() {
	filename := "drd4-tdah.fasta"
	data, err := os.ReadFile(filename)
	if err != nil {
		panic(err)
	}
	content := string(data)

	// load config (optional file ./config.json)
	cfg, _ := config.LoadConfig("")

	// configure logger output
	var loggerOut io.Writer = os.Stderr
	var logFileHandle *os.File
	if cfg.LogFile != "" {
		if f, err := os.OpenFile(cfg.LogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
			// write to both stderr and file so running interactively still shows logs
			loggerOut = io.MultiWriter(os.Stderr, f)
			logFileHandle = f
			// keep file handle open until program exit
			defer func() { _ = logFileHandle.Close() }()
		}
	}
	logger := log.New(loggerOut)

	// replace logger's writer with timestamping writer (defined at package level)
	logger = log.New(&timestampWriter{w: loggerOut})

	// apply log level from config (default: info)
	switch strings.ToLower(cfg.LogLevel) {
	case "debug":
		logger.SetLevel(log.DebugLevel)
	case "info", "":
		logger.SetLevel(log.InfoLevel)
	case "warn", "warning":
		logger.SetLevel(log.WarnLevel)
	case "error":
		logger.SetLevel(log.ErrorLevel)
	default:
		logger.SetLevel(log.InfoLevel)
		logger.Warn("unknown log_level in config.json, defaulting to info", "provided", cfg.LogLevel)
	}

	// startup log with non-sensitive config
	// Debug: show loaded config (avoid printing secrets)
	logger.Debug("loaded config", "input_fasta", cfg.InputFasta, "output_json", cfg.OutputJSON, "log_file", cfg.LogFile, "log_level", cfg.LogLevel, "use_external_translator", cfg.UseExternalTranslator)
	if cfg.LogFile != "" {
		if logFileHandle != nil {
			logger.Debug("log file open for append", "path", cfg.LogFile)
		} else {
			logger.Warn("log_file specified but could not be opened; logging to stderr only", "path", cfg.LogFile)
		}
	}
	logger.Info("starting drd4", "input_fasta", cfg.InputFasta, "output_json", cfg.OutputJSON, "log_file", cfg.LogFile, "ncbi_cache_path", cfg.NcbiCachePath, "ncbi_cache_ttl_secs", cfg.NcbiCacheTTLSecs)

	// allow config to override input filename
	if cfg.InputFasta != "" {
		filename = cfg.InputFasta
	}

	// apply ncbi config
	if cfg.NcbiCachePath != "" {
		ncbi.SetCacheFilePath(cfg.NcbiCachePath)
		logger.Debug("ncbi cache path set", "path", cfg.NcbiCachePath)
	}
	if cfg.NcbiApiKey != "" {
		// set the API key directly from config.json (config-only mode)
		os.Setenv("NCBI_API_KEY", cfg.NcbiApiKey)
		logger.Info("ncbi api key set from config.json (value not logged)")
		logger.Debug("ncbi api key provided in config (not logged)")
	}
	if cfg.NcbiCacheTTLSecs > 0 {
		ncbi.SetCacheTTLSeconds(cfg.NcbiCacheTTLSecs)
	}

	if filepath.Ext(filename) == ".fasta" {
		data, err = os.ReadFile(filename)
		if err != nil {
			logger.Fatal("failed to read input fasta", "path", filename, "err", err)
		}
		content = string(data)

		records := fasta.ParseFasta(strings.NewReader(content))
		logger.Info("parsed fasta", "path", filename, "records", len(records))

		// Try to run MAFFT on the original FASTA file to get aligned sequences.
		alignMap := make(map[string]string)
		if mpath, err := exec.LookPath("mafft"); err == nil {
			logger.Debug("mafft path", "path", mpath)
			logger.Info("mafft found, running alignment")
			cmd := exec.Command(mpath, "--auto", filename)
			logger.Debug("running command", "cmd", strings.Join(cmd.Args, " "))
			out, err := cmd.Output()
			if err != nil {
				logger.Error("mafft failed", "err", err)
			} else {
				logger.Debug("mafft output size", "bytes", len(out))
				aligned := fasta.ParseFasta(strings.NewReader(string(out)))
				for _, a := range aligned {
					alignMap[a.Header] = a.Sequence
				}
				logger.Info("mafft finished", "aligned_records", len(aligned))
			}
		} else {
			logger.Warn("mafft not found in PATH; nucleotides_align field will contain unaligned sequence")
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
			logger.Debug("temp file created", "path", tmp.Name())
			for _, rec := range records {
				seq := rec.Sequence
				if a, ok := alignMap[rec.Header]; ok && a != "" {
					seq = a
				}
				fmt.Fprintf(tmp, ">%s\n%s\n", rec.Header, seq)
			}
			tmp.Close()
			logger.Info("wrote aligned sequences to temp file", "path", tmp.Name())
			defer func(p string) {
				_ = os.Remove(p)
				logger.Debug("removed temp file", "path", p)
			}(tmp.Name())
		} else {
			logger.Error("cannot create temp file for aligned FASTA", "err", tmpErr)
		}

		// translate using external tool: prefer transeq (EMBOSS), then seqkit
		protMap := make(map[string]string)
		if tmpErr == nil && cfg.UseExternalTranslator {
			// prefer transeq, then seqkit
			if tpath, err := exec.LookPath("transeq"); err == nil {
				logger.Info("using external translator", "tool", "transeq")
				logger.Debug("transeq path", "path", tpath)
				cmd := exec.Command(tpath, "-sequence", tmp.Name(), "-outseq", "-")
				logger.Debug("running command", "cmd", strings.Join(cmd.Args, " "))
				out, err := cmd.Output()
				if err != nil {
					logger.Error("transeq failed", "err", err)
				} else {
					logger.Debug("transeq output size", "bytes", len(out))
					prots := fasta.ParseFasta(strings.NewReader(string(out)))
					for _, p := range prots {
						protMap[p.Header] = p.Sequence
					}
					logger.Info("transeq produced proteins", "count", len(prots))
				}
			} else if tpath, err := exec.LookPath("seqkit"); err == nil {
				logger.Info("using external translator", "tool", "seqkit")
				logger.Debug("seqkit path", "path", tpath)
				cmd := exec.Command(tpath, "translate", "-w", "0", tmp.Name())
				logger.Debug("running command", "cmd", strings.Join(cmd.Args, " "))
				out, err := cmd.Output()
				if err != nil {
					logger.Error("seqkit translate failed", "err", err)
				} else {
					logger.Debug("seqkit output size", "bytes", len(out))
					prots := fasta.ParseFasta(strings.NewReader(string(out)))
					for _, p := range prots {
						protMap[p.Header] = p.Sequence
					}
					logger.Info("seqkit produced proteins", "count", len(prots))
				}
			} else {
				logger.Warn("no external translator found (transeq or seqkit); translated field will be omitted")
			}
		} else if !cfg.UseExternalTranslator {
			logger.Info("external translator disabled by config; skipping translation step")
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
				logger.Debug("looking up translation in NCBI", "acc", acc)
				if gb, err := ncbi.FetchTranslationFromGenBank(acc); err != nil {
					logger.Warn("ncbi fetch error", "acc", acc, "err", err)
				} else if gb != "" {
					translation = gb
					logger.Debug("ncbi translation found", "acc", acc)
				} else {
					logger.Debug("ncbi translation not found", "acc", acc)
				}
			}
			// Fallback: use translation from external translator if GenBank not available
			if translation == "" {
				if t, ok := protMap[record.Header]; ok {
					logger.Debug("using external translation", "header", record.Header)
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
			logger.Fatal("json marshal failed", "err", err)
		}
		outPath := "drd4-database.json"
		if cfg.OutputJSON != "" {
			outPath = cfg.OutputJSON
		}

		if err := os.WriteFile(outPath, jsonData, 0o644); err != nil {
			logger.Error("failed to write output JSON", "path", outPath, "err", err)
		} else {
			logger.Info("wrote output JSON", "path", outPath, "variants", len(variants))
		}
	} else {
		fmt.Println(content)
	}
}
