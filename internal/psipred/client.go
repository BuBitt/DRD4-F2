package psipred

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

// SubmitResponse mirrors the minimal expected JSON response from PSIPRED submit
type SubmitResponse struct {
	UUID    string `json:"UUID"`
	State   string `json:"state"`
	Message string `json:"message"`
}

// SubmitJob submits a FASTA (as bytes) to the PSIPRED API and returns the UUID on success.
// baseURL should be like https://bioinf.cs.ucl.ac.uk/psipred/api
func SubmitJob(ctx context.Context, baseURL, job, submissionName, email string, fasta []byte, extraParams map[string]string) (string, error) {
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
	}

	// build multipart form
	buf := &bytes.Buffer{}
	mw := multipart.NewWriter(buf)

	// required fields
	_ = mw.WriteField("job", job)
	_ = mw.WriteField("submission_name", submissionName)
	_ = mw.WriteField("email", email)

	for k, v := range extraParams {
		_ = mw.WriteField(k, v)
	}

	fw, err := mw.CreateFormFile("input_data", "input.fasta")
	if err != nil {
		return "", err
	}
	if _, err := fw.Write(fasta); err != nil {
		return "", err
	}
	_ = mw.Close()

	submitURL := strings.TrimRight(baseURL, "/") + "/submission.json"
	req, err := http.NewRequestWithContext(ctx, "POST", submitURL, buf)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("psipred submit failed: %s: %s", resp.Status, string(body))
	}

	var out SubmitResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("failed to parse psipred response: %v (body: %s)", err, string(body))
	}
	if out.UUID == "" {
		return "", fmt.Errorf("psipred submission rejected: %s", out.Message)
	}
	return out.UUID, nil
}
