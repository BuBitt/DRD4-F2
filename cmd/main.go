package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"drd4/internal/config"
	"drd4/internal/fasta"
	"drd4/internal/ncbi"
	"drd4/internal/translator"

	"github.com/charmbracelet/log"
)

// version is the program version. It can be overridden at build time with -ldflags "-X main.version=..."
var version = "0.1.0"

// ReferenceHeader is the FASTA header used as the canonical reference during merges.
// Adjust this value to match the header present in your input FASTA when merging.
const ReferenceHeader = "NM_000797.4 Homo sapiens dopamine receptor D4 (DRD4), mRNA"

// timestampWriter prefixes each flushed line with an RFC3339 timestamp.
type timestampWriter struct {
	w   io.Writer
	buf bytes.Buffer
	mu  sync.Mutex
}

// Write buffers bytes until a newline is found; for each full line, write a timestamped
// line to the underlying writer. Partial lines are kept in the buffer.
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

// terminalWriter wraps an io.Writer and exposes an Fd method so libraries that
// inspect the file descriptor (for TTY detection) can work with wrapped writers.
type terminalWriter struct {
	w  io.Writer
	fd uintptr
}

func (tw *terminalWriter) Write(p []byte) (int, error) { return tw.w.Write(p) }

// Fd exposes the underlying file descriptor (e.g., os.Stderr.Fd()).
func (tw *terminalWriter) Fd() uintptr { return tw.fd }

func main() {
	// CLI flags
	inputFlag := flag.String("in", "drd4-tdah.fasta", "input FASTA file path")
	outputFlag := flag.String("out", "database.json", "output JSON file path")
	configFlag := flag.String("config", "", "path to config.json (optional)")
	externalFlag := flag.Bool("external", false, "enable external translator fallback (transeq/seqkit)")
	mafftArgs := flag.String("mafft-args", "--auto", "additional arguments to pass to mafft (quoted)")
	dryRun := flag.Bool("dry-run", false, "perform a dry run without writing outputs or calling external tools")
	verbose := flag.Bool("verbose", false, "enable verbose (debug) logging")
	versionFlag := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *versionFlag {
		fmt.Println("drd4", version)
		return
	}

	// load config (optional file)
	cfg, _ := config.LoadConfig(*configFlag)

	// merge CLI flags into config (flags override config when provided)
	if *inputFlag != "" {
		cfg.InputFasta = *inputFlag
	}
	if *outputFlag != "" {
		cfg.OutputJSON = *outputFlag
	}
	if *externalFlag {
		cfg.UseExternalTranslator = true
	}

	// initialize file vars used by legacy logic
	filename := ""
	var data []byte
	var err error
	var content string
	if cfg.InputFasta != "" {
		filename = cfg.InputFasta
	} else if *inputFlag != "" {
		filename = *inputFlag
	}
	if filename != "" {
		data, err = os.ReadFile(filename)
		if err == nil {
			content = string(data)
		}
	}

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
	// If stderr is a terminal-like device, force colors for libraries that honor FORCE_COLOR.
	if fi, err := os.Stderr.Stat(); err == nil {
		if fi.Mode()&os.ModeCharDevice != 0 {
			_ = os.Setenv("FORCE_COLOR", "1")
		}
	}
	// create logger backed by the timestamping writer and expose Fd so charm.log can detect TTY
	tw := &timestampWriter{w: loggerOut}
	termW := &terminalWriter{w: tw, fd: os.Stderr.Fd()}
	logger := log.New(termW)

	// apply log level from flags/config (flags override config)
	if *verbose {
		logger.SetLevel(log.DebugLevel)
	} else {
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
		absPath, aerr := filepath.Abs(cfg.NcbiCachePath)
		if aerr == nil {
			ncbi.SetCacheFilePath(absPath)
			logger.Info("ncbi cache path set from config (absolute)", "path", absPath)
		} else {
			ncbi.SetCacheFilePath(cfg.NcbiCachePath)
			logger.Info("ncbi cache path set from config", "path", cfg.NcbiCachePath)
		}
		defer ncbi.FlushCache()
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
		if *dryRun {
			logger.Info("dry-run: skipping mafft invocation")
		} else if mpath, err := exec.LookPath("mafft"); err == nil {
			logger.Debug("mafft path", "path", mpath)
			logger.Info("mafft found, running alignment")
			// run mafft with a 10-minute timeout and capture combined output
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			start := time.Now()
			// support passing additional args via --mafft-args (space separated)
			args := append(strings.Fields(*mafftArgs), filename)
			out, err := exec.CommandContext(ctx, mpath, args...).CombinedOutput()
			dur := time.Since(start)
			if err != nil {
				logger.Error("mafft failed or timed out", "err", err, "duration_ms", dur.Milliseconds(), "out_size", len(out))
			} else {
				logger.Debug("mafft finished", "duration_ms", dur.Milliseconds(), "out_size", len(out))
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
			Name               string `json:"name"`
			VariantCode        string `json:"variant_code"`
			PBCount            int    `json:"pb_count,omitempty"`
			AACount            int    `json:"aa_count,omitempty"`
			TranslationSource  string `json:"translation_source,omitempty"`
			Nucleotides        string `json:"nucleotides"`
			Translated         string `json:"translated,omitempty"`
			NucleotidesAlign   string `json:"nucleotides_align"`
			TranslateAlign     string `json:"translate_align,omitempty"`
			TranslateMergedRef string `json:"translate_merged_ref,omitempty"`
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

		// We no longer perform a bulk translation step here.
		// Translation will be resolved from NCBI (FetchTranslations). If a given
		// accession has no translation in GenBank, and external translation is
		// enabled, the program will invoke `seqkit translate` for that specific
		// variant only. This keeps translation responsibility outside the TUI
		// and avoids embedding biological translation logic in Go code.

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

			// don't fetch here; collect and resolve in batches later
			variants = append(variants, Variant{
				Name:             record.Header,
				VariantCode:      acc,
				Nucleotides:      record.Sequence,
				Translated:       "",
				NucleotidesAlign: alignSeq,
			})
		}

		// build list of accessions for NCBI lookup
		accessions := []string{}
		for _, v := range variants {
			if v.VariantCode != "" {
				accessions = append(accessions, v.VariantCode)
			}
		}

		// prepare concurrency/qps/batch defaults
		concurrency := cfg.NcbiConcurrency
		if concurrency <= 0 {
			concurrency = 8
		}
		qps := cfg.NcbiQPS
		if qps <= 0 {
			qps = 3
		}
		batchSize := cfg.NcbiBatchSize
		if batchSize <= 0 {
			batchSize = 10
		}

		logger.Info("starting ncbi batch lookup", "accessions", len(accessions), "concurrency", concurrency, "qps", qps, "batch_size", batchSize)

		// simple rate limiter: ticker at qps (use NewTicker to avoid leaking goroutine)
		ticker := time.NewTicker(time.Second / time.Duration(qps))
		defer ticker.Stop()

		// worker pool over batches
		tasks := make(chan []string)
		results := make(chan map[string]string)

		var wg sync.WaitGroup
		for i := 0; i < concurrency; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for batch := range tasks {
					<-ticker.C // rate limit per batch
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					m, err := ncbi.FetchTranslations(ctx, batch)
					cancel()
					if err != nil {
						logger.Warn("ncbi batch fetch error", "err", err)
					}
					results <- m
				}
			}()
		}

		// dispatch batches
		go func() {
			for i := 0; i < len(accessions); i += batchSize {
				end := i + batchSize
				if end > len(accessions) {
					end = len(accessions)
				}
				tasks <- accessions[i:end]
			}
			close(tasks)
		}()

		// collect results and fill variants map
		received := 0
		expected := (len(accessions) + batchSize - 1) / batchSize
		merged := map[string]string{}
		for received < expected {
			m := <-results
			for k, v := range m {
				merged[k] = v
			}
			received++
		}
		close(results)
		wg.Wait()

		// fill translations into variants. If NCBI has no translation for a
		// specific accession and external translation is enabled, invoke
		// `seqkit translate` for that single variant only. This keeps the
		// heavy translation work off the TUI and uses well-tested external
		// tools for the biological translation.
		var seqkitPath string
		if cfg.UseExternalTranslator && !*dryRun {
			if sp, err := exec.LookPath("seqkit"); err == nil {
				seqkitPath = sp
				logger.Info("seqkit available for per-variant translation", "path", seqkitPath)
			} else {
				logger.Info("seqkit not found in PATH; per-variant external translation disabled")
			}
		} else if *dryRun {
			logger.Info("dry-run: skipping per-variant external translation")
		}

		ncbiCount := 0
		seqkitCount := 0
		ncbiTotalPB := 0
		ncbiTotalAA := 0
		// First assign NCBI translations and collect records that need seqkit
		missingForSeqkit := []translator.TranslatorRecord{}
		for i := range variants {
			acc := variants[i].VariantCode
			if acc != "" {
				if t, ok := merged[acc]; ok && t != "" {
					variants[i].Translated = t
					variants[i].TranslationSource = "ncbi"
					// try to read cached metadata (pb/aa)
					if _, pb, aa, ok2 := ncbi.GetCachedMetadata(acc); ok2 {
						variants[i].PBCount = pb
						variants[i].AACount = aa
						ncbiTotalPB += pb
						ncbiTotalAA += aa
					}
					ncbiCount++
					continue
				}
			}
			if seqkitPath != "" {
				missingForSeqkit = append(missingForSeqkit, translator.TranslatorRecord{Index: i, Header: variants[i].Name, Sequence: variants[i].Nucleotides})
			}
		}

		seqkitTotalPB := 0
		seqkitTotalAA := 0
		if len(missingForSeqkit) > 0 && seqkitPath != "" {
			translatedMap, _ := translator.TranslateMissing(missingForSeqkit, seqkitPath, 15*time.Second)
			for idx, seq := range translatedMap {
				variants[idx].Translated = seq
				variants[idx].TranslationSource = "seqkit"
				// seqkit produced protein sequence; record AA count
				variants[idx].AACount = len(seq)
				// set PB to nucleotide length when available
				if variants[idx].NucleotidesAlign != "" {
					variants[idx].PBCount = len(variants[idx].NucleotidesAlign)
				} else {
					variants[idx].PBCount = len(variants[idx].Nucleotides)
				}
				seqkitCount++
				seqkitTotalAA += len(seq)
			}
		}

		// Log counts of translation sources and aggregated PB/AA from NCBI and seqkit
		logger.Info("translation sources summary", "ncbi_translations", ncbiCount, "seqkit_translations", seqkitCount, "ncbi_total_pb", ncbiTotalPB, "ncbi_total_aa", ncbiTotalAA, "seqkit_total_pb", seqkitTotalPB, "seqkit_total_aa", seqkitTotalAA)

		// Build protein FASTA from existing translations for alignment
		protTmp, perr := os.CreateTemp("", "prot-*.fasta")
		if perr != nil {
			logger.Error("failed to create temp file for protein alignment", "err", perr)
		} else {
			// write translated sequences; if a variant lacks translation, write empty placeholder
			for i, v := range variants {
				seq := v.Translated
				if seq == "" {
					seq = strings.Repeat("-", 1) // placeholder single gap
				}
				fmt.Fprintf(protTmp, ">%d|%s\n%s\n", i, v.VariantCode, seq)
			}
			protTmp.Close()

			// Run MAFFT on protein FASTA (required by request)
			mpath, merr := exec.LookPath("mafft")
			if merr != nil {
				logger.Fatal("mafft not found in PATH; protein alignment required")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			args := append(strings.Fields(*mafftArgs), protTmp.Name())
			out, err := exec.CommandContext(ctx, mpath, args...).CombinedOutput()
			if err != nil {
				logger.Fatal("mafft protein alignment failed", "err", err, "output", string(out))
			}
			alignedProt := fasta.ParseFasta(strings.NewReader(string(out)))

			// map aligned proteins back to variants
			for _, ap := range alignedProt {
				// header format: index|variant
				parts := strings.SplitN(ap.Header, "|", 2)
				if len(parts) < 1 {
					continue
				}
				idx := 0
				fmt.Sscanf(parts[0], "%d", &idx)
				if idx >= 0 && idx < len(variants) {
					variants[idx].TranslateAlign = ap.Sequence
				}
			}

			_ = os.Remove(protTmp.Name())
		}

		// Identify reference aligned translation (use first variant matching ReferenceHeader or 0)
		refIdx := 0
		for i, v := range variants {
			if strings.Contains(v.Name, ReferenceHeader) {
				refIdx = i
				break
			}
		}
		refAlign := variants[refIdx].TranslateAlign

		// Merge: for each variant, replace '-' in TranslateAlign with corresponding AA from refAlign
		for i := range variants {
			va := variants[i].TranslateAlign
			if va == "" {
				variants[i].TranslateMergedRef = ""
				continue
			}
			merged := []rune{}
			max := len(refAlign)
			if len(va) > max {
				max = len(va)
			}
			for j := 0; j < max; j++ {
				var c rune = '-'
				if j < len(va) {
					c = rune(va[j])
				}
				if c == '-' {
					if j < len(refAlign) {
						r := rune(refAlign[j])
						if r != '-' {
							merged = append(merged, r)
							continue
						}
					}
					merged = append(merged, '-')
					continue
				}
				merged = append(merged, c)
			}
			variants[i].TranslateMergedRef = string(merged)
		}

		jsonData, err := json.MarshalIndent(variants, "", "  ")
		if err != nil {
			logger.Fatal("json marshal failed", "err", err)
		}
		outPath := "database.json"
		if cfg.OutputJSON != "" {
			outPath = cfg.OutputJSON
		}

		if *dryRun {
			logger.Info("dry-run: would write output JSON", "path", outPath, "variants", len(variants))
		} else {
			if err := os.WriteFile(outPath, jsonData, 0o644); err != nil {
				logger.Error("failed to write output JSON", "path", outPath, "err", err)
			} else {
				logger.Info("wrote output JSON", "path", outPath, "variants", len(variants))
			}
		}
	} else {
		fmt.Println(content)
	}
}
