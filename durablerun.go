// Package serializefunctions provides function serialization and a durable
// (persistent, crash-resistant) task runner.
//
// It allows registering Go functions, serializing calls with their parameters
// as JSON, and executing them later — even after process crashes — using
// DurableRunner.
package serializefunctions

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"
	"time"
)

// ── Status ────────────────────────────────────────────────────────────────────

// FuncStatus represents the current state of a durable task.
type FuncStatus string

const (
	// StatusPending means the task is queued but not yet started.
	StatusPending FuncStatus = "pending"
	// StatusRunning means the task is currently being executed.
	StatusRunning FuncStatus = "running"
	// StatusDone means the task completed successfully.
	StatusDone FuncStatus = "done"
	// StatusFailed means the task failed (either by error or panic).
	StatusFailed FuncStatus = "failed"
)

// ── Persisted record ──────────────────────────────────────────────────────────

// DurableRecord represents a single persistent task with full execution metadata.
type DurableRecord struct {
	Func      Function   `json:"func"`
	Status    FuncStatus `json:"status"`
	AddedAt   time.Time  `json:"added_at"`
	StartedAt *time.Time `json:"started_at,omitempty"`
	DoneAt    *time.Time `json:"done_at,omitempty"`
	Error     string     `json:"error,omitempty"`
	CrashLog  string     `json:"crash_log,omitempty"`
}

// ── DurableRunner ─────────────────────────────────────────────────────────────

// DurableRunner provides durable, crash-resistant execution of serialized functions.
// Tasks are persisted to a JSON file and automatically recovered on restart.
type DurableRunner struct {
	filePath string
	threads  int

	mu      sync.Mutex
	records map[int]*DurableRecord
	taskCh  chan *DurableRecord
	wg      sync.WaitGroup
}

// NewDurableRunner creates a new durable runner.
//
// filePath: path to the JSON persistence file.
// threads:  number of concurrent workers.
func NewDurableRunner(filePath string, threads int) *DurableRunner {
	dr := &DurableRunner{
		filePath: filePath,
		threads:  threads,
		records:  make(map[int]*DurableRecord),
		taskCh:   make(chan *DurableRecord, 256),
	}
	dr.load()
	return dr
}

// Start launches the worker pool and re-queues any pending or running tasks
// from the previous run (crash recovery).
func (dr *DurableRunner) Start() {
	for i := 0; i < dr.threads; i++ {
		dr.wg.Add(1)
		go dr.worker(i)
	}

	// Recover tasks that were in progress when the process last terminated
	dr.mu.Lock()
	for _, rec := range dr.records {
		if rec.Status == StatusPending || rec.Status == StatusRunning {
			rec.Status = StatusPending
			rec.CrashLog = fmt.Sprintf("Recovered from crash — was %s at startup", rec.Status)
			dr.taskCh <- rec
		}
	}
	dr.mu.Unlock()

	dr.save()

	log.Printf("[DurableRunner] Started with %d threads, persistence file: %s", dr.threads, dr.filePath)
}

// Stop gracefully shuts down all workers and saves final state.
func (dr *DurableRunner) Stop() {
	close(dr.taskCh)
	dr.wg.Wait()
	dr.save()
	log.Println("[DurableRunner] Stopped cleanly")
}

// ── Task Assignment ───────────────────────────────────────────────────────────

// AssignDurableRun queues a serialized function for durable execution.
// The task is immediately persisted and scheduled.
func (dr *DurableRunner) AssignDurableRun(f Function) {
	rec := &DurableRecord{
		Func:    f,
		Status:  StatusPending,
		AddedAt: time.Now(),
	}

	dr.mu.Lock()
	dr.records[f.SaveID] = rec
	dr.mu.Unlock()

	dr.save()
	dr.taskCh <- rec
}

// ── Status Queries ────────────────────────────────────────────────────────────

// Status returns the current state of a task by its SaveID.
func (dr *DurableRunner) Status(id int) (DurableRecord, bool) {
	dr.mu.Lock()
	defer dr.mu.Unlock()
	rec, ok := dr.records[id]
	if !ok {
		return DurableRecord{}, false
	}
	return *rec, true
}

// StatusAll returns a snapshot of all tracked tasks.
func (dr *DurableRunner) StatusAll() []DurableRecord {
	dr.mu.Lock()
	defer dr.mu.Unlock()
	out := make([]DurableRecord, 0, len(dr.records))
	for _, rec := range dr.records {
		out = append(out, *rec)
	}
	return out
}

// CrashReport returns all tasks that failed or have crash recovery logs.
func (dr *DurableRunner) CrashReport() []DurableRecord {
	dr.mu.Lock()
	defer dr.mu.Unlock()
	var out []DurableRecord
	for _, rec := range dr.records {
		if rec.Status == StatusFailed || rec.CrashLog != "" {
			out = append(out, *rec)
		}
	}
	return out
}

// PrintCrashReport prints a human-readable crash report to stdout.
func (dr *DurableRunner) PrintCrashReport() {
	report := dr.CrashReport()
	if len(report) == 0 {
		fmt.Println("[CrashReport] No crashes recorded.")
		return
	}
	fmt.Printf("[CrashReport] %d crash(es) found:\n", len(report))
	for _, rec := range report {
		fmt.Printf(" ID: %d | Func: %s | Status: %s | Error: %s | CrashLog: %s\n",
			rec.Func.SaveID, rec.Func.Fun, rec.Status, rec.Error, rec.CrashLog)
	}
}

// ── Internal Worker ───────────────────────────────────────────────────────────

func (dr *DurableRunner) worker(id int) {
	defer dr.wg.Done()
	log.Printf("[Worker %d] Ready", id)

	for rec := range dr.taskCh {
		dr.execute(id, rec)
	}
	log.Printf("[Worker %d] Shut down", id)
}

// execute runs a single task with proper status tracking and panic recovery.
func (dr *DurableRunner) execute(workerID int, rec *DurableRecord) {
	now := time.Now()

	// Mark as running
	dr.mu.Lock()
	rec.Status = StatusRunning
	rec.StartedAt = &now
	dr.mu.Unlock()
	dr.save()

	log.Printf("[Worker %d] Running function %d (%s)", workerID, rec.Func.SaveID, rec.Func.Fun)

	var execErr error
	func() {
		defer func() {
			if r := recover(); r != nil {
				execErr = fmt.Errorf("panic: %v", r)
			}
		}()
		_, execErr = ExecuteFunc[any](rec.Func)
	}()

	done := time.Now()

	dr.mu.Lock()
	rec.DoneAt = &done
	if execErr != nil {
		rec.Status = StatusFailed
		rec.Error = execErr.Error()
		rec.CrashLog = fmt.Sprintf("Worker %d caught at %s: %v", workerID, done.Format(time.RFC3339), execErr)
		log.Printf("[Worker %d] Function %d FAILED: %v", workerID, rec.Func.SaveID, execErr)
	} else {
		rec.Status = StatusDone
		log.Printf("[Worker %d] Function %d done", workerID, rec.Func.SaveID)
	}
	dr.mu.Unlock()
	dr.save()
}

// ── Persistence ───────────────────────────────────────────────────────────────

type persistedStore struct {
	Records map[int]*DurableRecord `json:"records"`
}

// save persists only non-completed tasks to disk.
func (dr *DurableRunner) save() {
	dr.mu.Lock()
	defer dr.mu.Unlock()

	toSave := make(map[int]*DurableRecord)
	for id, rec := range dr.records {
		if rec.Status != StatusDone {
			toSave[id] = rec
		}
	}

	data, err := json.MarshalIndent(persistedStore{Records: toSave}, "", "  ")
	if err != nil {
		log.Printf("[DurableRunner] save error: %v", err)
		return
	}

	if err := os.WriteFile(dr.filePath, data, 0644); err != nil {
		log.Printf("[DurableRunner] write error: %v", err)
	}
}

// load restores persisted tasks from disk on startup.
func (dr *DurableRunner) load() {
	data, err := os.ReadFile(dr.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		log.Printf("[DurableRunner] load error: %v", err)
		return
	}

	var store persistedStore
	if err := json.Unmarshal(data, &store); err != nil {
		log.Printf("[DurableRunner] parse error: %v", err)
		return
	}

	dr.mu.Lock()
	dr.records = store.Records
	dr.mu.Unlock()

	log.Printf("[DurableRunner] Loaded %d records from %s", len(store.Records), dr.filePath)
}
