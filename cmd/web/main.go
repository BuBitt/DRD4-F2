package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"drd4/internal/psipred"
)

type Variant struct {
	Name                   string   `json:"name"`
	VariantCode            string   `json:"variant_code"`
	PBCount                int      `json:"pb_count,omitempty"`
	AACount                int      `json:"aa_count,omitempty"`
	TranslationSource      string   `json:"translation_source,omitempty"`
	UnsubstitutedPositions []string `json:"unsubstituted_positions,omitempty"`
	Nucleotides            string   `json:"nucleotides"`
	Translated             string   `json:"translated,omitempty"`
	NucleotidesAlign       string   `json:"nucleotides_align"`
	TranslateAlign         string   `json:"translate_align,omitempty"`
	TranslateMergedRef     string   `json:"translate_merged_ref,omitempty"`
	PsipredUUID            string   `json:"psipred_uuid,omitempty"`
}

// VariantsPage is used to render the base page and to carry query state
type VariantsPage struct {
	Variants []Variant
	Query    string
	Sort     string
	Role     string
}

var templates *template.Template

func loadTemplates(dir string) error {
	t := template.New("")
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".html") {
			rel, _ := filepath.Rel(dir, path)
			_, err := t.ParseFiles(path)
			if err != nil {
				return err
			}
			_ = rel
		}
		return nil
	})
	if err != nil {
		return err
	}
	templates = t
	return nil
}

// statusResponseWriter captures status and bytes written for logging
type statusResponseWriter struct {
	http.ResponseWriter
	status  int
	written int64
}

func (w *statusResponseWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusResponseWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.written += int64(n)
	return n, err
}

// loggingMiddleware logs each request with method, path, status, size and duration
func loggingMiddleware(logger *log.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		srw := &statusResponseWriter{ResponseWriter: w}
		next.ServeHTTP(srw, r)
		if srw.status == 0 {
			srw.status = http.StatusOK
		}
		duration := time.Since(start)
		logger.Printf("%s - %s %s %d %dB %s %q",
			r.RemoteAddr, r.Method, r.URL.RequestURI(), srw.status, srw.written, duration, r.UserAgent())
	})
}

// readDatabase reads and unmarshals the JSON file at path
func readDatabase(path string) ([]Variant, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var v []Variant
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, err
	}
	return v, nil
}

func indexHandler(dbPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// ler database e passar para o template para renderizar a lista inicialmente
		variants, err := readDatabase(dbPath)
		if err != nil {
			log.Printf("warning: failed to read database for index: %v", err)
			variants = []Variant{}
		}
		page := VariantsPage{Variants: variants, Query: r.URL.Query().Get("q"), Sort: r.URL.Query().Get("sort"), Role: extractRole(r)}
		if err := templates.ExecuteTemplate(w, "base.html", page); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
}

// extractRole determines the visitor role from headers, query or cookie.
// Reasonable defaults: header X-User-Role, query param 'role', cookie 'role', else 'guest'.
func extractRole(r *http.Request) string {
	if role := strings.TrimSpace(r.Header.Get("X-User-Role")); role != "" {
		return role
	}
	if role := strings.TrimSpace(r.URL.Query().Get("role")); role != "" {
		return role
	}
	if c, err := r.Cookie("role"); err == nil {
		if role := strings.TrimSpace(c.Value); role != "" {
			return role
		}
	}
	return "guest"
}

func variantsHandler(dbPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		variants, err := readDatabase(dbPath)
		if err != nil {
			http.Error(w, "failed to read database", http.StatusInternalServerError)
			return
		}
		q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
		sortMode := r.URL.Query().Get("sort")

		// filter
		filtered := make([]Variant, 0, len(variants))
		for _, v := range variants {
			if q == "" {
				filtered = append(filtered, v)
				continue
			}
			if strings.Contains(strings.ToLower(v.VariantCode), q) || strings.Contains(strings.ToLower(v.Name), q) || strings.Contains(strings.ToLower(v.TranslationSource), q) {
				filtered = append(filtered, v)
			}
		}

		// sort
		switch sortMode {
		case "pb":
			sort.Slice(filtered, func(i, j int) bool { return filtered[i].PBCount > filtered[j].PBCount })
		case "name":
			sort.Slice(filtered, func(i, j int) bool { return strings.ToLower(filtered[i].Name) < strings.ToLower(filtered[j].Name) })
		default:
			sort.Slice(filtered, func(i, j int) bool {
				return strings.ToLower(filtered[i].VariantCode) < strings.ToLower(filtered[j].VariantCode)
			})
		}

		// render fragment (send only the slice)
		if err := templates.ExecuteTemplate(w, "variants.html", filtered); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
}

func variantHandler(dbPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(r.URL.Path, "/")
		if len(parts) < 3 || parts[2] == "" {
			http.Error(w, "missing variant", http.StatusBadRequest)
			return
		}
		code := parts[2]
		variants, err := readDatabase(dbPath)
		if err != nil {
			http.Error(w, "failed to read database", http.StatusInternalServerError)
			return
		}
		var found *Variant
		for _, v := range variants {
			if v.VariantCode == code {
				vv := v
				found = &vv
				break
			}
		}
		if found == nil {
			http.Error(w, "variant not found", http.StatusNotFound)
			return
		}
		// Se requisição for HX (fragment), renderiza apenas o fragmento; caso contrário, renderiza página inteira
		if r.Header.Get("HX-Request") == "true" || r.Header.Get("X-Requested-With") == "XMLHttpRequest" {
			if err := templates.ExecuteTemplate(w, "detail.html", found); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			return
		}
		if err := templates.ExecuteTemplate(w, "variant_page.html", found); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
}

// psipredJobHandler redirects to a simple page for a PSIPRED UUID or to database entry
func psipredJobHandler(dbPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(r.URL.Path, "/")
		if len(parts) < 4 || parts[3] == "" {
			http.Error(w, "missing uuid", http.StatusBadRequest)
			return
		}
		uuid := parts[3]
		// procura no database por esse uuid e redireciona para a variante correspondente se encontrada
		variants, err := readDatabase(dbPath)
		if err == nil {
			for _, v := range variants {
				if v.PsipredUUID == uuid {
					http.Redirect(w, r, "/variant/"+v.VariantCode, http.StatusSeeOther)
					return
				}
			}
		}
		// senão, mostrar uma página simples com link externo para a API
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, "<html><body><h1>PSIPRED job %s</h1><p>Nenhuma variante associada encontrada no database.</p></body></html>", uuid)
	}
}

// psipredSubmitHandler submits the variant's TranslateMergedRef to the PSIPRED API and
// stores the returned UUID back into database.json (simple read-modify-write).
func psipredSubmitHandler(dbPath, psipredBase, psipredEmail string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if psipredBase == "" || psipredEmail == "" {
			http.Error(w, "PSIPRED não configurado no servidor", http.StatusBadRequest)
			return
		}
		parts := strings.Split(r.URL.Path, "/")
		if len(parts) < 4 || parts[3] == "" {
			http.Error(w, "missing variant", http.StatusBadRequest)
			return
		}
		code := parts[3]
		variants, err := readDatabase(dbPath)
		if err != nil {
			http.Error(w, "failed to read database", http.StatusInternalServerError)
			return
		}
		var idx = -1
		for i, v := range variants {
			if v.VariantCode == code {
				idx = i
				break
			}
		}
		if idx < 0 {
			http.Error(w, "variant not found", http.StatusNotFound)
			return
		}
		seq := variants[idx].TranslateMergedRef
		if strings.TrimSpace(seq) == "" {
			http.Error(w, "variant sem TranslateMergedRef", http.StatusBadRequest)
			return
		}
		fasta := fmt.Sprintf(">%s\n%s\n", variants[idx].VariantCode, seq)
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		uuid, err := psipred.SubmitJob(ctx, psipredBase, "psipred", variants[idx].VariantCode, psipredEmail, []byte(fasta), nil)
		if err != nil {
			http.Error(w, fmt.Sprintf("psipred submit failed: %v", err), http.StatusInternalServerError)
			return
		}
		// persist uuid back to database
		variants[idx].PsipredUUID = uuid
		out, _ := json.MarshalIndent(variants, "", "  ")
		if err := os.WriteFile(dbPath, out, 0644); err != nil {
			log.Printf("warning: failed to write database.json: %v", err)
		}
		// return a small fragment with status and uuid
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"uuid": uuid})
	}
}

// psipredStatusHandler proxies a status check to PSIPRED for a given UUID
func psipredStatusHandler(psipredBase string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(r.URL.Path, "/")
		if len(parts) < 4 || parts[3] == "" {
			http.Error(w, "missing uuid", http.StatusBadRequest)
			return
		}
		uuid := parts[3]
		if psipredBase == "" {
			http.Error(w, "PSIPRED base não configurada no servidor", http.StatusBadRequest)
			return
		}
		reqURL := strings.TrimRight(psipredBase, "/") + "/submission/" + uuid
		cli := &http.Client{Timeout: 30 * time.Second}
		resp, err := cli.Get(reqURL)
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to contact psipred: %v", err), http.StatusInternalServerError)
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}
}

// psipredJobsHandler shows a simple table of variants that have a PSIPRED UUID
func psipredJobsHandler(dbPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		variants, err := readDatabase(dbPath)
		if err != nil {
			http.Error(w, "failed to read database", http.StatusInternalServerError)
			return
		}
		jobs := make([]Variant, 0, 8)
		for _, v := range variants {
			if strings.TrimSpace(v.PsipredUUID) != "" {
				jobs = append(jobs, v)
			}
		}
		if err := templates.ExecuteTemplate(w, "psipred_jobs.html", jobs); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
}

// apiVariantHandler returns JSON for a single variant
func apiVariantHandler(dbPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(r.URL.Path, "/")
		if len(parts) < 3 || parts[2] == "" {
			http.Error(w, "missing variant", http.StatusBadRequest)
			return
		}
		code := parts[2]
		variants, err := readDatabase(dbPath)
		if err != nil {
			http.Error(w, "failed to read database", http.StatusInternalServerError)
			return
		}
		for _, v := range variants {
			if v.VariantCode == code {
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				_ = json.NewEncoder(w).Encode(v)
				return
			}
		}
		http.Error(w, "variant not found", http.StatusNotFound)
	}
}

// apiPsipredJobsHandler returns JSON list of variants that have a PSIPRED UUID
func apiPsipredJobsHandler(dbPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		variants, err := readDatabase(dbPath)
		if err != nil {
			http.Error(w, "failed to read database", http.StatusInternalServerError)
			return
		}
		jobs := make([]Variant, 0, 8)
		for _, v := range variants {
			if strings.TrimSpace(v.PsipredUUID) != "" {
				jobs = append(jobs, v)
			}
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(jobs)
	}
}

func main() {
	addr := flag.String("addr", ":8080", "endereço HTTP para servir")
	dbPath := flag.String("db", "database.json", "caminho para database.json")
	templatesDir := flag.String("templates", "web/templates", "diretório de templates HTML")
	psipredBase := flag.String("psipred-base", "https://bioinf.cs.ucl.ac.uk/psipred/api", "URL base da API PSIPRED")
	psipredEmail := flag.String("psipred-email", "", "email para submissão ao PSIPRED (opcional para UI)")
	logFile := flag.String("log", "", "path to write access logs (optional). If empty, logs go to stdout only")
	flag.Parse()

	if err := loadTemplates(*templatesDir); err != nil {
		log.Fatalf("failed to load templates: %v", err)
	}

	// prepare mux so we can wrap with middleware
	mux := http.NewServeMux()
	fs := http.FileServer(http.Dir("web/static"))
	mux.Handle("/static/", http.StripPrefix("/static/", fs))
	mux.HandleFunc("/", indexHandler(*dbPath))
	mux.HandleFunc("/variants", variantsHandler(*dbPath))
	mux.HandleFunc("/variant/", variantHandler(*dbPath))
	mux.HandleFunc("/psipred/submit/", psipredSubmitHandler(*dbPath, *psipredBase, *psipredEmail))
	mux.HandleFunc("/psipred/status/", psipredStatusHandler(*psipredBase))
	mux.HandleFunc("/psipred/job/", psipredJobHandler(*dbPath))
	mux.HandleFunc("/psipred-jobs", psipredJobsHandler(*dbPath))
	// API endpoints for SPA-like interactions
	mux.HandleFunc("/api/variant/", apiVariantHandler(*dbPath))
	mux.HandleFunc("/api/psipred/jobs", apiPsipredJobsHandler(*dbPath))

	// configure logger
	var out io.Writer = os.Stdout
	if *logFile != "" {
		f, err := os.OpenFile(*logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			log.Fatalf("failed to open log file: %v", err)
		}
		out = io.MultiWriter(os.Stdout, f)
	}
	logger := log.New(out, "drd4: ", log.LstdFlags)

	// wrap mux with logging middleware
	handler := loggingMiddleware(logger, mux)

	srv := &http.Server{Addr: *addr, Handler: handler, ReadTimeout: 5 * time.Second, WriteTimeout: 10 * time.Second}
	fmt.Printf("serving HTMX UI at http://%s/ (db=%s)\n", *addr, *dbPath)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}
}
