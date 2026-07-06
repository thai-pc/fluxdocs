package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// waitJob polls a MemQueue until the job reaches a terminal state or times out.
func waitJob(t *testing.T, q *MemQueue, org, id string) Job {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		j, err := q.Get(context.Background(), org, id)
		if err == nil && (j.Status == JobComplete || j.Status == JobFailed) {
			return j
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("job %s did not finish in time", id)
	return Job{}
}

func TestMemQueueRunsHandler(t *testing.T) {
	q := NewMemQueue(2)
	defer q.Stop()
	q.Register("echo", func(_ context.Context, org string, args map[string]any) (string, error) {
		return "doc-" + org + "-" + args["n"].(string), nil
	})
	id, err := q.Enqueue(context.Background(), "org-a", "echo", map[string]any{"n": "1"})
	if err != nil {
		t.Fatal(err)
	}
	job := waitJob(t, q, "org-a", id)
	if job.Status != JobComplete || job.ResultDocID != "doc-org-a-1" {
		t.Fatalf("job = %+v", job)
	}
}

func TestMemQueueHandlerError(t *testing.T) {
	q := NewMemQueue(1)
	defer q.Stop()
	q.Register("boom", func(context.Context, string, map[string]any) (string, error) {
		return "", fmt.Errorf("kaboom")
	})
	id, _ := q.Enqueue(context.Background(), "org-a", "boom", nil)
	job := waitJob(t, q, "org-a", id)
	if job.Status != JobFailed || !strings.Contains(job.Error, "kaboom") {
		t.Fatalf("job = %+v", job)
	}
}

func TestMemQueueUnknownKind(t *testing.T) {
	q := NewMemQueue(1)
	defer q.Stop()
	id, _ := q.Enqueue(context.Background(), "org-a", "nope", nil)
	job := waitJob(t, q, "org-a", id)
	if job.Status != JobFailed {
		t.Fatalf("unknown kind should fail: %+v", job)
	}
}

func TestMemQueueOrgIsolation(t *testing.T) {
	q := NewMemQueue(1)
	defer q.Stop()
	q.Register("x", func(context.Context, string, map[string]any) (string, error) { return "d", nil })
	id, _ := q.Enqueue(context.Background(), "org-a", "x", nil)
	// org-b must not see org-a's job.
	if _, err := q.Get(context.Background(), "org-b", id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-org Get err = %v, want ErrNotFound", err)
	}
}

// TestAsyncRedactPIIFlow drives the HTTP async path end to end.
func TestAsyncRedactPIIFlow(t *testing.T) {
	s, key := testServer(t, nil)
	q := NewMemQueue(2)
	defer q.Stop()
	s.SetQueue(q)

	pdf := contentPDF("BT /F1 12 Tf 72 700 Td (SSN 123-45-6789) Tj 0 -20 Td (public text) Tj ET")
	_, up := do(t, s, http.MethodPost, "/v1/documents", key, pdf)
	id := up["id"].(string)

	// Enqueue -> 202 + job id.
	code, acc := do(t, s, http.MethodPost, "/v1/documents/"+id+"/redact-pii-async", key, []byte(`{"kinds":["ssn"]}`))
	if code != http.StatusAccepted {
		t.Fatalf("enqueue = %d %v", code, acc)
	}
	jobID := acc["job_id"].(string)

	// Poll the job endpoint until it completes.
	var resultDoc string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_, jb := do(t, s, http.MethodGet, "/v1/jobs/"+jobID, key, nil)
		if jb["status"] == string(JobComplete) {
			resultDoc, _ = jb["result_document_id"].(string)
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if resultDoc == "" {
		t.Fatal("job did not complete with a result document")
	}

	// The redacted result must have the SSN gone, public text kept.
	_, ext := do(t, s, http.MethodPost, "/v1/documents/"+resultDoc+"/extract", key, nil)
	text := ext["text"].(string)
	if strings.Contains(text, "123-45-6789") {
		t.Errorf("LEAK: SSN survived async redaction: %q", text)
	}
	if !strings.Contains(text, "public text") {
		t.Errorf("over-redacted: %q", text)
	}
}

func TestAsyncEndpointsDisabledWithoutQueue(t *testing.T) {
	s, key := testServer(t, nil) // no SetQueue
	_, up := do(t, s, http.MethodPost, "/v1/documents", key, textPDF("x"))
	id := up["id"].(string)
	if code, _ := do(t, s, http.MethodPost, "/v1/documents/"+id+"/redact-pii-async", key, nil); code != http.StatusNotImplemented {
		t.Errorf("async without queue: code = %d, want 501", code)
	}
	if code, _ := do(t, s, http.MethodGet, "/v1/jobs/whatever", key, nil); code != http.StatusNotFound {
		t.Errorf("job lookup without queue: code = %d, want 404", code)
	}
}
