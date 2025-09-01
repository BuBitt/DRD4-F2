package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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
	"sync"
	"time"

	"database/sql"
	"drd4/internal/psipred"

	_ "modernc.org/sqlite"
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

// PSIPRED job persistence
type PsipredJob struct {
	ID          string    `json:"id"`
	VariantCode string    `json:"variant_code"`
	RemoteUUID  string    `json:"remote_uuid,omitempty"`
	State       string    `json:"state"` // queued, submitting, submitted, polling, complete, error
	Message     string    `json:"message,omitempty"`
	Email       string    `json:"email,omitempty"`
	RemoteState string    `json:"remote_state,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

var jobsPath string
var jobsMu sync.Mutex
var jobsStore string // "json" or "sqlite"
var jobsDB *sql.DB
var pollInterval time.Duration
var pollTimeout time.Duration

// PollerInfo holds runtime information about a poller for a given remote UUID
type PollerInfo struct {
	Cancel     context.CancelFunc `json:"-"`
	JobID      string             `json:"job_id"`
	StartedAt  time.Time          `json:"started_at"`
	AcquiredAt *time.Time         `json:"acquired_at,omitempty"`
	Status     string             `json:"status"` // waiting, running
}

var jobsPollers = map[string]*PollerInfo{}
var jobsPollersMu sync.Mutex

// semaphore to limit concurrent pollers
var pollerSem chan struct{}
var pollerMax int

// audit log path for state change events
var auditLogPath string = "psipred_audit.log"
var auditMu sync.Mutex
var auditMaxBytes int64 = 10 * 1024 * 1024 // 10 MB
var auditMaxBackups int = 5
var psipredBaseURL string

// initJobsDB ensures the sqlite database file exists, opens a connection and ensures schema
func initJobsDB(path string) error {
	if jobsDB != nil {
		return nil
	}
	// ensure parent directory exists
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create jobs dir: %v", err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return fmt.Errorf("failed to open sqlite db: %v", err)
	}
	// set a reasonable busy timeout via pragmas to reduce errors on concurrent writes
	if _, err := db.Exec("PRAGMA busy_timeout = 5000;"); err != nil {
		// non-fatal
	}
	// create schema if not exists
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS jobs (
			id TEXT PRIMARY KEY,
			variant_code TEXT,
			remote_uuid TEXT,
			state TEXT,
			message TEXT,
			email TEXT,
			created_at TEXT,
			updated_at TEXT
		)`)
	if err != nil {
		db.Close()
		return fmt.Errorf("failed to ensure jobs table: %v", err)
	}
	// migrate existing table: ensure 'email' column exists
	cols, err := db.Query("PRAGMA table_info(jobs);")
	if err == nil {
		found := false
		for cols.Next() {
			var cid int
			var name, ctype string
			var notnull, dfltValue, pk interface{}
			_ = cols.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk)
			if name == "email" {
				found = true
				break
			}
		}
		cols.Close()
		if !found {
			if _, err := db.Exec("ALTER TABLE jobs ADD COLUMN email TEXT"); err != nil {
				// log and continue
				log.Printf("failed to add email column to jobs table: %v", err)
			} else {
				log.Printf("migrated jobs table: added email column")
			}
		}
	}
	jobsDB = db
	return nil
}

func loadJobs(path string) ([]PsipredJob, error) {
	if jobsStore == "sqlite" {
		// ensure DB is initialized (creates file/table if necessary)
		if jobsDB == nil {
			if err := initJobsDB(path); err != nil {
				var out []PsipredJob
				return out, err
			}
		}
		// load from sqlite
		var out []PsipredJob
		if jobsDB == nil {
			return out, fmt.Errorf("sqlite db not initialized")
		}
		rows, err := jobsDB.Query("SELECT id, variant_code, remote_uuid, state, message, email, created_at, updated_at FROM jobs ORDER BY created_at DESC")
		if err != nil {
			return out, err
		}
		defer rows.Close()
		for rows.Next() {
			var j PsipredJob
			var created, updated string
			var email sql.NullString
			if err := rows.Scan(&j.ID, &j.VariantCode, &j.RemoteUUID, &j.State, &j.Message, &email, &created, &updated); err != nil {
				return out, err
			}
			if email.Valid {
				j.Email = email.String
			}
			j.CreatedAt, _ = time.Parse(time.RFC3339, created)
			j.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
			out = append(out, j)
		}
		return out, nil
	}
	var out []PsipredJob
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return out, err
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return out, err
	}
	return out, nil
}

func saveJobs(path string, jobs []PsipredJob) error {
	if jobsStore == "sqlite" {
		// ensure DB is initialized (creates file/table if necessary)
		if jobsDB == nil {
			if err := initJobsDB(path); err != nil {
				return err
			}
		}
		if jobsDB == nil {
			return fmt.Errorf("sqlite db not initialized")
		}
		tx, err := jobsDB.Begin()
		if err != nil {
			return err
		}
		// clear and insert
		if _, err := tx.Exec("DELETE FROM jobs"); err != nil {
			tx.Rollback()
			return err
		}
		stmt, err := tx.Prepare("INSERT INTO jobs(id, variant_code, remote_uuid, state, message, email, created_at, updated_at) VALUES(?,?,?,?,?,?,?,?)")
		if err != nil {
			tx.Rollback()
			return err
		}
		defer stmt.Close()
		for _, j := range jobs {
			if _, err := stmt.Exec(j.ID, j.VariantCode, j.RemoteUUID, j.State, j.Message, j.Email, j.CreatedAt.Format(time.RFC3339), j.UpdatedAt.Format(time.RFC3339)); err != nil {
				tx.Rollback()
				return err
			}
		}
		return tx.Commit()
	}
	data, err := json.MarshalIndent(jobs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// helper to persist job update safely
func persistJobUpdate(path string, update func([]PsipredJob) ([]PsipredJob, error)) error {
	jobsMu.Lock()
	defer jobsMu.Unlock()
	jobs, err := loadJobs(path)
	if err != nil {
		return err
	}
	// keep a shallow copy of old states to detect changes for audit
	oldMap := map[string]PsipredJob{}
	for _, j := range jobs {
		oldMap[j.ID] = j
	}
	jobs, err = update(jobs)
	if err != nil {
		return err
	}
	// detect state/message changes and append to audit log
	for _, nj := range jobs {
		if oj, ok := oldMap[nj.ID]; ok {
			if oj.State != nj.State || oj.Message != nj.Message {
				// log change to the audit file (JSON line)
				entry := map[string]interface{}{
					"timestamp":    time.Now().Format(time.RFC3339),
					"job_id":       nj.ID,
					"variant_code": nj.VariantCode,
					"from_state":   oj.State,
					"to_state":     nj.State,
					"from_message": oj.Message,
					"to_message":   nj.Message,
				}
				// write asynchronously but protected by a mutex for file safety
				go auditAppend(entry)
			}
		} else {
			// new job created — record creation event
			entry := map[string]interface{}{
				"timestamp":    time.Now().Format(time.RFC3339),
				"job_id":       nj.ID,
				"variant_code": nj.VariantCode,
				"event":        "created",
				"state":        nj.State,
			}
			go auditAppend(entry)
		}
	}
	return saveJobs(path, jobs)
}

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

// generateRandomEmail returns a pseudo-random email for job submissions
func generateRandomEmail() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("user+%d@example.com", time.Now().UnixNano())
	}
	return fmt.Sprintf("user+%s@example.com", hex.EncodeToString(b))
}

// cleanSequence extracts sequence lines from a possible FASTA-like input, removes any header
// lines (those starting with '>'), concatenates the remaining lines, and keeps only the
// 20 standard amino-acid letters ACDEFGHIKLMNPQRSTVWY (uppercase).
func cleanSequence(s string) string {
	// split into lines and ignore header lines
	var raw strings.Builder
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, ">") {
			// skip header
			continue
		}
		raw.WriteString(line)
	}
	// keep only allowed amino-acid letters
	allowed := map[byte]bool{}
	for _, c := range "ACDEFGHIKLMNPQRSTVWY" {
		allowed[byte(c)] = true
	}
	var out strings.Builder
	for i := 0; i < raw.Len(); i++ {
		ch := raw.String()[i]
		// uppercase letters
		if ch >= 'a' && ch <= 'z' {
			ch = ch - 'a' + 'A'
		}
		if allowed[ch] {
			out.WriteByte(ch)
		}
	}
	return out.String()
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
		// senão, procurar no store de jobs persistidos
		if jobs, jerr := loadJobs(jobsPath); jerr == nil {
			for _, job := range jobs {
				if job.RemoteUUID == uuid {
					w.Header().Set("Content-Type", "text/html; charset=utf-8")
					// build links
					external := "https://bioinf.cs.ucl.ac.uk/psipred/&uuid=" + uuid
					remoteLink := "/psipred/status/" + uuid
					fmt.Fprintf(w, "<html><body><h1>PSIPRED job %s</h1>", uuid)
					fmt.Fprintf(w, "<p><strong>Job ID:</strong> %s<br>", job.ID)
					if job.VariantCode != "" {
						fmt.Fprintf(w, "<strong>Variant:</strong> <a href=\"/variant/%s\">%s</a><br>", job.VariantCode, job.VariantCode)
					}
					fmt.Fprintf(w, "<strong>Status:</strong> %s</p>", job.State)
					fmt.Fprintf(w, "<p><a href=\"%s\" target=\"_blank\">Abrir PSIPRED (externo)</a> — <a href=\"%s\">Ver status remoto</a></p>", external, remoteLink)
					fmt.Fprintf(w, "</body></html>")
					return
				}
			}
		}

		// se nenhum job encontrado, mostrar mensagem padrão
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, "<html><body><h1>PSIPRED job %s</h1><p>Nenhuma variante associada encontrada no database.</p></body></html>", uuid)
	}
}

// psipredSubmitHandler submits the variant's TranslateMergedRef to the PSIPRED API and
// stores the returned UUID back into database.json (simple read-modify-write).
func psipredSubmitHandler(dbPath, psipredBase, psipredEmail string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if psipredBase == "" {
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
		// sanitize sequence to contain only amino-acid letters
		clean := cleanSequence(seq)
		if clean == "" {
			http.Error(w, "variant TranslateMergedRef não contém aminoácidos válidos", http.StatusBadRequest)
			return
		}
		// create a persisted job record (queued) and return job ID immediately
		// PSIPRED expects only the amino-acid sequence content (no FASTA header). Send only the sequence.
		fasta := fmt.Sprintf("%s\n", clean)
		jobID := fmt.Sprintf("job-%d-%d", time.Now().UnixNano(), os.Getpid())
		// generate random email per requirement and store with job
		randEmail := generateRandomEmail()
		job := PsipredJob{
			ID:          jobID,
			VariantCode: variants[idx].VariantCode,
			State:       "queued",
			Email:       randEmail,
			CreatedAt:   time.Now(),
			UpdatedAt:   time.Now(),
		}
		// persist job
		if err := persistJobUpdate(jobsPath, func(js []PsipredJob) ([]PsipredJob, error) {
			js = append(js, job)
			return js, nil
		}); err != nil {
			http.Error(w, "failed to persist job", http.StatusInternalServerError)
			return
		}

		// background submit: do not block request
		go func(j PsipredJob, fastaBytes []byte) {
			// mark submitting
			_ = persistJobUpdate(jobsPath, func(js []PsipredJob) ([]PsipredJob, error) {
				for i := range js {
					if js[i].ID == j.ID {
						js[i].State = "submitting"
						js[i].UpdatedAt = time.Now()
					}
				}
				return js, nil
			})
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			// use the job's email when submitting
			uuid, err := psipred.SubmitJob(ctx, psipredBase, "psipred", j.VariantCode, j.Email, fastaBytes, nil)
			if err != nil {
				_ = persistJobUpdate(jobsPath, func(js []PsipredJob) ([]PsipredJob, error) {
					for i := range js {
						if js[i].ID == j.ID {
							js[i].State = "error"
							js[i].Message = err.Error()
							js[i].UpdatedAt = time.Now()
						}
					}
					return js, nil
				})
				return
			}
			// update job with remote uuid and mark submitted
			_ = persistJobUpdate(jobsPath, func(js []PsipredJob) ([]PsipredJob, error) {
				for i := range js {
					if js[i].ID == j.ID {
						js[i].RemoteUUID = uuid
						js[i].State = "submitted"
						js[i].Email = j.Email
						js[i].RemoteState = "submitted"
						js[i].UpdatedAt = time.Now()
					}
				}
				return js, nil
			})
			// Also persist the remote UUID into database.json for the matching variant
			go func(uuid string, variantCode string, dbPath string) {
				variants, err := readDatabase(dbPath)
				if err != nil {
					return
				}
				updated := false
				for i := range variants {
					if variants[i].VariantCode == variantCode {
						variants[i].PsipredUUID = uuid
						updated = true
						break
					}
				}
				if !updated {
					return
				}
				// write back database.json (atomic write)
				tmp := dbPath + ".tmp"
				b, err := json.MarshalIndent(variants, "", "  ")
				if err != nil {
					return
				}
				if err := os.WriteFile(tmp, b, 0644); err != nil {
					return
				}
				_ = os.Rename(tmp, dbPath)
			}(uuid, j.VariantCode, dbPath)

			// start a background poller for this job (if not already running)
			startJobPoller(uuid, j.ID)
			// optional: start polling and update state to complete/error (light polling)
			pollCtx, pollCancel := context.WithTimeout(context.Background(), pollTimeout)
			defer pollCancel()
			for {
				select {
				case <-pollCtx.Done():
					_ = persistJobUpdate(jobsPath, func(js []PsipredJob) ([]PsipredJob, error) {
						for i := range js {
							if js[i].ID == j.ID {
								js[i].State = "error"
								js[i].Message = "poll timeout"
								js[i].UpdatedAt = time.Now()
							}
						}
						return js, nil
					})
					return
				case <-time.After(pollInterval):
					// query remote status
					reqURL := strings.TrimRight(psipredBase, "/") + "/submission/" + uuid
					cli := &http.Client{Timeout: 20 * time.Second}
					resp, err := cli.Get(reqURL)
					if err != nil {
						continue
					}
					body, _ := io.ReadAll(resp.Body)
					resp.Body.Close()
					var m map[string]interface{}
					if err := json.Unmarshal(body, &m); err != nil {
						continue
					}
					state := ""
					if s, ok := m["state"].(string); ok {
						state = s
					}
					if strings.EqualFold(state, "Complete") {
						_ = persistJobUpdate(jobsPath, func(js []PsipredJob) ([]PsipredJob, error) {
							for i := range js {
								if js[i].ID == j.ID {
									js[i].State = "complete"
									js[i].RemoteState = "complete"
									js[i].UpdatedAt = time.Now()
								}
							}
							return js, nil
						})
						return
					}
					if strings.EqualFold(state, "Error") {
						_ = persistJobUpdate(jobsPath, func(js []PsipredJob) ([]PsipredJob, error) {
							for i := range js {
								if js[i].ID == j.ID {
									js[i].State = "error"
									js[i].RemoteState = "error"
									if v, ok := m["last_message"].(string); ok {
										js[i].Message = v
									}
									js[i].UpdatedAt = time.Now()
								}
							}
							return js, nil
						})
						return
					}
				}
			}
		}(job, []byte(fasta))

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"job_id": jobID})
	}
}

// api to fetch job status by job id
func apiPsipredJobHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// extract last path segment as job id
		p := strings.TrimSuffix(r.URL.Path, "/")
		idx := strings.LastIndex(p, "/")
		if idx < 0 || idx+1 >= len(p) {
			http.Error(w, "missing job id", http.StatusBadRequest)
			return
		}
		id := p[idx+1:]
		jobs, err := loadJobs(jobsPath)
		if err != nil {
			http.Error(w, "failed to read jobs", http.StatusInternalServerError)
			return
		}
		for _, j := range jobs {
			if j.ID == id {
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				_ = json.NewEncoder(w).Encode(j)
				return
			}
		}
		http.Error(w, "job not found", http.StatusNotFound)
	}
}

// api to list persisted jobs
func apiPsipredJobsListHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		jobs, err := loadJobs(jobsPath)
		if err != nil {
			http.Error(w, "failed to read jobs", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(jobs)
	}
}

// startJobPoller begins polling remote PSIPRED for the given remote UUID and ties it to local jobID.
// It ensures only one poller runs per remote UUID.
func startJobPoller(remoteUUID, jobID string) {
	jobsPollersMu.Lock()
	if _, ok := jobsPollers[remoteUUID]; ok {
		jobsPollersMu.Unlock()
		return
	}
	// create cancellable context
	ctx, cancel := context.WithCancel(context.Background())
	pi := &PollerInfo{Cancel: cancel, JobID: jobID, StartedAt: time.Now(), Status: "waiting"}
	jobsPollers[remoteUUID] = pi
	jobsPollersMu.Unlock()

	// audit: record poller registration
	go func(uuid, jid string) {
		auditMu.Lock()
		defer auditMu.Unlock()
		e := map[string]interface{}{"timestamp": time.Now().Format(time.RFC3339), "event": "poller_registered", "remote_uuid": uuid, "job_id": jid}
		b, _ := json.Marshal(e)
		f, err := os.OpenFile(auditLogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
		if err == nil {
			_, _ = f.Write(append(b, '\n'))
			f.Close()
		}
	}(remoteUUID, jobID)

	go func() {
		// on exit, remove poller entry and audit
		defer func() {
			jobsPollersMu.Lock()
			delete(jobsPollers, remoteUUID)
			jobsPollersMu.Unlock()
			// audit poller stopped
			go func(uuid, jid string) {
				auditMu.Lock()
				defer auditMu.Unlock()
				e := map[string]interface{}{"timestamp": time.Now().Format(time.RFC3339), "event": "poller_stopped", "remote_uuid": uuid, "job_id": jid}
				b, _ := json.Marshal(e)
				f, err := os.OpenFile(auditLogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
				if err == nil {
					_, _ = f.Write(append(b, '\n'))
					f.Close()
				}
			}(remoteUUID, jobID)
		}()

		cli := &http.Client{Timeout: 20 * time.Second}

		// Acquire semaphore to limit concurrent pollers if configured
		acquired := false
		if pollerSem != nil {
			select {
			case pollerSem <- struct{}{}:
				acquired = true
				now := time.Now()
				jobsPollersMu.Lock()
				if p, ok := jobsPollers[remoteUUID]; ok {
					p.AcquiredAt = &now
					p.Status = "running"
				}
				jobsPollersMu.Unlock()
			case <-ctx.Done():
				return
			}
			defer func() {
				if acquired {
					<-pollerSem
				}
			}()
		} else {
			// mark running immediately
			jobsPollersMu.Lock()
			if p, ok := jobsPollers[remoteUUID]; ok {
				p.Status = "running"
			}
			jobsPollersMu.Unlock()
		}

		ticker := time.NewTicker(pollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				reqURL := strings.TrimRight(psipredBaseURL, "/") + "/submission/" + remoteUUID
				resp, err := cli.Get(reqURL)
				if err != nil {
					continue
				}
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				var m map[string]interface{}
				if err := json.Unmarshal(body, &m); err != nil {
					continue
				}
				state := ""
				if s, ok := m["state"].(string); ok {
					state = s
				}
				// persist remote state and update local state accordingly
				_ = persistJobUpdate(jobsPath, func(js []PsipredJob) ([]PsipredJob, error) {
					for i := range js {
						if js[i].ID == jobID {
							js[i].RemoteState = state
							// map remote state to local state if final
							if strings.EqualFold(state, "Complete") {
								js[i].State = "complete"
								js[i].UpdatedAt = time.Now()
							} else if strings.EqualFold(state, "Error") {
								js[i].State = "error"
								js[i].UpdatedAt = time.Now()
							} else {
								js[i].State = "submitted"
								js[i].UpdatedAt = time.Now()
							}
						}
					}
					return js, nil
				})
				if strings.EqualFold(state, "Complete") || strings.EqualFold(state, "Error") {
					return
				}
			}
		}
	}()
}

// auditAppend writes a JSON entry to the audit log and rotates when size exceeds threshold
func auditAppend(entry map[string]interface{}) {
	auditMu.Lock()
	defer auditMu.Unlock()
	b, _ := json.Marshal(entry)
	// ensure directory exists
	if dir := filepath.Dir(auditLogPath); dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0755)
	}
	// rotate if needed
	if fi, err := os.Stat(auditLogPath); err == nil {
		if fi.Size() >= auditMaxBytes {
			// rotate: rename to .1, .2 ... keeping auditMaxBackups
			for i := auditMaxBackups - 1; i >= 1; i-- {
				older := fmt.Sprintf("%s.%d", auditLogPath, i)
				newer := fmt.Sprintf("%s.%d", auditLogPath, i+1)
				if _, err := os.Stat(older); err == nil {
					_ = os.Rename(older, newer)
				}
			}
			// move current to .1
			_ = os.Rename(auditLogPath, fmt.Sprintf("%s.1", auditLogPath))
		}
	}
	f, err := os.OpenFile(auditLogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("AUDIT-ERR: failed to open audit log: %v", err)
		return
	}
	defer f.Close()
	_, _ = f.Write(append(b, '\n'))
}

// admin page for pollers (full page)
func pollersAdminHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// prepare view model with human-readable timestamps (same shape as fragment handler)
		jobsPollersMu.Lock()
		view := map[string]map[string]string{}
		for uuid, p := range jobsPollers {
			m := map[string]string{}
			variant := resolveVariantFromJob(p.JobID)
			if variant == "" {
				variant = p.JobID
			}
			m["variant_id"] = variant
			m["status"] = p.Status
			if !p.StartedAt.IsZero() {
				m["started_at"] = p.StartedAt.Format("2006-01-02 15:04:05")
			} else {
				m["started_at"] = "-"
			}
			if p.AcquiredAt != nil && !p.AcquiredAt.IsZero() {
				m["acquired_at"] = p.AcquiredAt.Format("2006-01-02 15:04:05")
			} else {
				m["acquired_at"] = "-"
			}
			view[uuid] = m
		}
		jobsPollersMu.Unlock()
		if err := templates.ExecuteTemplate(w, "pollers.html", view); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
}

// fragment for HTMX to refresh pollers table
func pollersFragmentHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		jobsPollersMu.Lock()
		// prepare a human-friendly view model
		view := map[string]map[string]string{}
		for uuid, p := range jobsPollers {
			m := map[string]string{}
			variant := resolveVariantFromJob(p.JobID)
			if variant == "" {
				variant = p.JobID
			}
			m["variant_id"] = variant
			m["status"] = p.Status
			if !p.StartedAt.IsZero() {
				m["started_at"] = p.StartedAt.Format("02 Jan 2006 15:04")
				m["started_rel"] = humanAgo(p.StartedAt)
			} else {
				m["started_at"] = "-"
				m["started_rel"] = "-"
			}
			if p.AcquiredAt != nil && !p.AcquiredAt.IsZero() {
				m["acquired_at"] = p.AcquiredAt.Format("02 Jan 2006 15:04")
				m["acquired_rel"] = humanAgo(*p.AcquiredAt)
			} else {
				m["acquired_at"] = "-"
				m["acquired_rel"] = "-"
			}
			view[uuid] = m
		}
		jobsPollersMu.Unlock()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := templates.ExecuteTemplate(w, "pollers_fragment.html", view); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
}

// resolveVariantFromJob looks up persisted jobs to find the variant code for a job id.
func resolveVariantFromJob(jobID string) string {
	jobs, err := loadJobs(jobsPath)
	if err != nil {
		return ""
	}
	for _, j := range jobs {
		if j.ID == jobID {
			return j.VariantCode
		}
	}
	return ""
}

// cancel a poller by remote UUID
func cancelPollerHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(r.URL.Path, "/")
		if len(parts) < 5 || parts[4] == "" {
			http.Error(w, "missing uuid", http.StatusBadRequest)
			return
		}
		uuid := parts[4]
		jobsPollersMu.Lock()
		pi, ok := jobsPollers[uuid]
		jobsPollersMu.Unlock()
		if !ok {
			http.Error(w, "poller not found", http.StatusNotFound)
			return
		}
		// cancel
		if pi.Cancel != nil {
			pi.Cancel()
		}
		// audit
		go auditAppend(map[string]interface{}{"timestamp": time.Now().Format(time.RFC3339), "event": "poller_cancelled", "remote_uuid": uuid, "job_id": pi.JobID})
		w.WriteHeader(http.StatusNoContent)
	}
}

// humanAgo returns a short Portuguese relative time like "agora", "há 3m", "há 1h".
func humanAgo(t time.Time) string {
	d := time.Since(t)
	if d < time.Minute {
		return "agora"
	}
	if d < time.Hour {
		m := int(d.Minutes())
		return fmt.Sprintf("há %dm", m)
	}
	if d < 24*time.Hour {
		h := int(d.Hours())
		return fmt.Sprintf("há %dh", h)
	}
	days := int(d.Hours() / 24)
	return fmt.Sprintf("há %dd", days)
}

// api to list active pollers and semaphore status
func apiPollersHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		jobsPollersMu.Lock()
		out := map[string]map[string]interface{}{}
		for uuid, p := range jobsPollers {
			info := map[string]interface{}{
				"job_id":     p.JobID,
				"started_at": p.StartedAt.Format(time.RFC3339),
				"status":     p.Status,
			}
			if p.AcquiredAt != nil {
				info["acquired_at"] = p.AcquiredAt.Format(time.RFC3339)
			}
			out[uuid] = info
		}
		jobsPollersMu.Unlock()

		// semaphore info
		semInfo := map[string]interface{}{
			"max_concurrent": pollerMax,
		}
		if pollerSem != nil {
			semInfo["in_use"] = pollerMax - len(pollerSem)
		} else {
			semInfo["in_use"] = 0
		}

		resp := map[string]interface{}{
			"pollers":   out,
			"semaphore": semInfo,
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(resp)
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
func psipredJobsHandler(dbPath string, psipredBase string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// load persisted jobs (JSON or sqlite) and render them
		jobs, err := loadJobs(jobsPath)
		if err != nil {
			log.Printf("error loading psipred jobs: %v", err)
			http.Error(w, "failed to read psipred jobs", http.StatusInternalServerError)
			return
		}
		// optionally, for entries that have RemoteUUID but no RemoteState, try a single quick probe
		for i := range jobs {
			if jobs[i].RemoteUUID != "" && jobs[i].RemoteState == "" {
				reqURL := strings.TrimRight(psipredBase, "/") + "/submission/" + jobs[i].RemoteUUID
				cli := &http.Client{Timeout: 5 * time.Second}
				resp, err := cli.Get(reqURL)
				if err == nil {
					body, _ := io.ReadAll(resp.Body)
					resp.Body.Close()
					var m map[string]interface{}
					if err := json.Unmarshal(body, &m); err == nil {
						if s, ok := m["state"].(string); ok {
							jobs[i].RemoteState = s
						}
					}
				}
			}
		}
		if err := templates.ExecuteTemplate(w, "psipred_jobs.html", jobs); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
}

// fragment handler for HTMX to refresh only the jobs table
func psipredJobsFragmentHandler(dbPath string, psipredBase string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		jobs, err := loadJobs(jobsPath)
		if err != nil {
			http.Error(w, "failed to read psipred jobs", http.StatusInternalServerError)
			return
		}
		// fill RemoteState as above (single quick probe)
		for i := range jobs {
			if jobs[i].RemoteUUID != "" && jobs[i].RemoteState == "" {
				reqURL := strings.TrimRight(psipredBase, "/") + "/submission/" + jobs[i].RemoteUUID
				cli := &http.Client{Timeout: 5 * time.Second}
				resp, err := cli.Get(reqURL)
				if err == nil {
					body, _ := io.ReadAll(resp.Body)
					resp.Body.Close()
					var m map[string]interface{}
					if err := json.Unmarshal(body, &m); err == nil {
						if s, ok := m["state"].(string); ok {
							jobs[i].RemoteState = s
						}
					}
				}
			}
		}
		// render only the fragment - we reuse the same template but instruct callers to swap by outerHTML
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// execute the whole template so it includes the table; HTMX will replace the element
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
		// return persisted jobs as JSON
		jobs, err := loadJobs(jobsPath)
		if err != nil {
			log.Printf("error loading psipred jobs (api): %v", err)
			http.Error(w, "failed to read psipred jobs", http.StatusInternalServerError)
			return
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
	psipredJobsFlag := flag.String("psipred-jobs", "psipred_jobs.json", "arquivo para persistir estados de jobs PSIPRED")
	jobsStoreFlag := flag.String("psipred-store", "json", "psipred jobs store: 'json' or 'sqlite'")
	pollSec := flag.Int("psipred-poll-sec", 30, "intervalo de polling (segundos) para checar status remoto")
	pollTimeoutMin := flag.Int("psipred-poll-timeout-min", 60, "timeout de polling (minutos) para aguardar job")
	pollersMax := flag.Int("psipred-pollers-max", 5, "maximo de pollers concorrentes (0 = ilimitado)")
	webFlag := flag.Bool("web", false, "run with sensible web defaults (sqlite store, poll=10s, timeout=30m, access.log)")
	flag.Parse()

	// If --web is provided, override relevant flags to production defaults
	if webFlag != nil && *webFlag {
		*jobsStoreFlag = "sqlite"
		*psipredJobsFlag = "psipred_jobs.db"
		*pollSec = 10
		*pollTimeoutMin = 30
		if *logFile == "" {
			*logFile = "access.log"
		}
		// note: addr and templates keep their values unless explicitly changed
	}

	jobsStore = strings.ToLower(strings.TrimSpace(*jobsStoreFlag))
	if jobsStore != "json" && jobsStore != "sqlite" {
		log.Fatalf("invalid psipred-store: %s", *jobsStoreFlag)
	}
	jobsPath = *psipredJobsFlag
	pollInterval = time.Duration(*pollSec) * time.Second
	pollTimeout = time.Duration(*pollTimeoutMin) * time.Minute
	pollerMax = *pollersMax
	if pollerMax > 0 {
		pollerSem = make(chan struct{}, pollerMax)
	}
	// initialize sqlite if requested
	if jobsStore == "sqlite" {
		db, err := sql.Open("sqlite", jobsPath)
		if err != nil {
			log.Fatalf("failed to open sqlite db: %v", err)
		}
		jobsDB = db
		// create schema if not exists
		_, err = jobsDB.Exec(`CREATE TABLE IF NOT EXISTS jobs (
			id TEXT PRIMARY KEY,
			variant_code TEXT,
			remote_uuid TEXT,
			state TEXT,
			message TEXT,
			email TEXT,
			created_at TEXT,
			updated_at TEXT
		)`)
		if err != nil {
			log.Fatalf("failed to ensure jobs table: %v", err)
		}
		// migrate existing table: ensure 'email' column exists
		cols, err := jobsDB.Query("PRAGMA table_info(jobs);")
		if err == nil {
			found := false
			for cols.Next() {
				var cid int
				var name, ctype string
				var notnull, dfltValue, pk interface{}
				_ = cols.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk)
				if name == "email" {
					found = true
					break
				}
			}
			cols.Close()
			if !found {
				if _, err := jobsDB.Exec("ALTER TABLE jobs ADD COLUMN email TEXT"); err != nil {
					log.Printf("failed to add email column to jobs table: %v", err)
				} else {
					log.Printf("migrated jobs table: added email column")
				}
			}
		}
	}

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
	mux.HandleFunc("/psipred-jobs", psipredJobsHandler(*dbPath, *psipredBase))
	mux.HandleFunc("/psipred-jobs/fragment", psipredJobsFragmentHandler(*dbPath, *psipredBase))
	// API endpoints for SPA-like interactions
	mux.HandleFunc("/api/variant/", apiVariantHandler(*dbPath))
	mux.HandleFunc("/api/psipred/jobs", apiPsipredJobsHandler(*dbPath))
	// job persistence APIs
	jobsPath = *psipredJobsFlag
	mux.HandleFunc("/api/psipred/job/", apiPsipredJobHandler())
	mux.HandleFunc("/api/psipred/jobs/list", apiPsipredJobsListHandler())
	mux.HandleFunc("/api/psipred/pollers", apiPollersHandler())
	mux.HandleFunc("/api/psipred/pollers/cancel/", cancelPollerHandler())
	mux.HandleFunc("/pollers", pollersAdminHandler())
	mux.HandleFunc("/pollers/fragment", pollersFragmentHandler())

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

	// log PSIPRED config for debugging
	logger.Printf("psipred.base=%q psipred.email=%q jobsStore=%q jobsPath=%q", *psipredBase, *psipredEmail, jobsStore, jobsPath)
	psipredBaseURL = *psipredBase

	// start pollers for jobs already persisted (if any)
	if jobsStore != "json" || true {
		if js, err := loadJobs(jobsPath); err == nil {
			for _, j := range js {
				if j.RemoteUUID != "" && !(strings.EqualFold(j.State, "complete") || strings.EqualFold(j.State, "error")) {
					startJobPoller(j.RemoteUUID, j.ID)
				}
			}
		}
	}
	// wrap mux with logging middleware
	handler := loggingMiddleware(logger, mux)

	srv := &http.Server{Addr: *addr, Handler: handler, ReadTimeout: 5 * time.Second, WriteTimeout: 10 * time.Second}
	fmt.Printf("serving HTMX UI at http://%s/ (db=%s)\n", *addr, *dbPath)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}
}
