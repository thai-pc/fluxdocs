package api

import (
	"context"
	"sync"
	"time"
)

// jobs.go defines the async job abstraction for long-running work (batch
// redaction, OCR) that should not block an HTTP request. The API is shaped like
// a durable queue — jobs are enqueued by kind with JSON-serializable args and
// executed by registered handlers — so the production backend (River, a
// PostgreSQL-backed queue) plugs in behind the same JobQueue interface. The
// in-process MemQueue here runs a bounded worker pool and is fully testable with
// no external services; it is not durable across restarts.

// JobStatus is the lifecycle state of a job.
type JobStatus string

const (
	JobPending  JobStatus = "pending"
	JobRunning  JobStatus = "running"
	JobComplete JobStatus = "complete"
	JobFailed   JobStatus = "failed"
)

// Job is a unit of background work and its outcome.
type Job struct {
	ID          string
	OrgID       string
	Kind        string
	Status      JobStatus
	ResultDocID string // document id produced on success, if any
	Error       string // failure message, if any
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// JobHandler executes one job. It returns the id of any document it produced.
type JobHandler func(ctx context.Context, orgID string, args map[string]any) (resultDocID string, err error)

// JobQueue enqueues background work and tracks its status, scoped by org.
type JobQueue interface {
	Register(kind string, h JobHandler)
	Enqueue(ctx context.Context, orgID, kind string, args map[string]any) (jobID string, err error)
	Get(ctx context.Context, orgID, jobID string) (Job, error) // ErrNotFound for wrong org / unknown id
}

// MemQueue is an in-process JobQueue backed by a bounded worker pool.
type MemQueue struct {
	mu       sync.RWMutex
	handlers map[string]JobHandler
	jobs     map[string]Job
	ch       chan queued
	wg       sync.WaitGroup
	stop     chan struct{}
}

type queued struct {
	jobID string
	args  map[string]any
}

// NewMemQueue starts a queue with the given number of workers (at least 1).
func NewMemQueue(workers int) *MemQueue {
	if workers < 1 {
		workers = 1
	}
	q := &MemQueue{
		handlers: map[string]JobHandler{},
		jobs:     map[string]Job{},
		ch:       make(chan queued, 256),
		stop:     make(chan struct{}),
	}
	for i := 0; i < workers; i++ {
		q.wg.Add(1)
		go q.worker()
	}
	return q
}

// Register installs the handler for a job kind. Not safe to call concurrently
// with Enqueue for the same kind; register all kinds at startup.
func (q *MemQueue) Register(kind string, h JobHandler) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.handlers[kind] = h
}

// Enqueue records a pending job and hands it to a worker.
func (q *MemQueue) Enqueue(_ context.Context, orgID, kind string, args map[string]any) (string, error) {
	id := newID()
	now := time.Now().UTC()
	q.mu.Lock()
	q.jobs[id] = Job{ID: id, OrgID: orgID, Kind: kind, Status: JobPending, CreatedAt: now, UpdatedAt: now}
	q.mu.Unlock()
	q.ch <- queued{jobID: id, args: args}
	return id, nil
}

// Get returns the job if it belongs to orgID, else ErrNotFound.
func (q *MemQueue) Get(_ context.Context, orgID, jobID string) (Job, error) {
	q.mu.RLock()
	defer q.mu.RUnlock()
	j, ok := q.jobs[jobID]
	if !ok || j.OrgID != orgID {
		return Job{}, ErrNotFound
	}
	return j, nil
}

// Stop shuts the pool down and waits for in-flight jobs to finish.
func (q *MemQueue) Stop() {
	close(q.stop)
	q.wg.Wait()
}

func (q *MemQueue) worker() {
	defer q.wg.Done()
	for {
		select {
		case <-q.stop:
			return
		case item := <-q.ch:
			q.run(item)
		}
	}
}

func (q *MemQueue) run(item queued) {
	q.mu.RLock()
	job := q.jobs[item.jobID]
	handler := q.handlers[job.Kind]
	q.mu.RUnlock()

	q.update(item.jobID, func(j *Job) { j.Status = JobRunning })

	if handler == nil {
		q.update(item.jobID, func(j *Job) { j.Status = JobFailed; j.Error = "no handler for kind " + j.Kind })
		return
	}
	resultDoc, err := handler(context.Background(), job.OrgID, item.args)
	q.update(item.jobID, func(j *Job) {
		if err != nil {
			j.Status, j.Error = JobFailed, err.Error()
			return
		}
		j.Status, j.ResultDocID = JobComplete, resultDoc
	})
}

func (q *MemQueue) update(id string, fn func(*Job)) {
	q.mu.Lock()
	defer q.mu.Unlock()
	j := q.jobs[id]
	fn(&j)
	j.UpdatedAt = time.Now().UTC()
	q.jobs[id] = j
}
