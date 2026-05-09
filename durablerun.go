package serializefunctions

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

type FuncStatus string

const (
	StatusPending   FuncStatus = "pending"
	StatusRunning   FuncStatus = "running"
	StatusDone      FuncStatus = "done"
	StatusFailed    FuncStatus = "failed"
	StatusScheduled FuncStatus = "scheduled"
)

type DurableRecord struct {
	Func      Function   `json:"func"`
	Status    FuncStatus `json:"status"`
	Schedule  string     `json:"schedule,omitempty"` // cron expression, empty = run once
	AddedAt   time.Time  `json:"added_at"`
	StartedAt *time.Time `json:"started_at,omitempty"`
	DoneAt    *time.Time `json:"done_at,omitempty"`
	Error     string     `json:"error,omitempty"`
	CrashLog  string     `json:"crash_log,omitempty"`
}

type DurableRunner struct {
	filePath string
	threads  int

	mu      sync.Mutex
	records map[int]*DurableRecord // keyed by Function.SaveID

	taskCh chan *DurableRecord
	wg     sync.WaitGroup

	cron   *cron.Cron
	stopCh chan struct{}
}

// NewDurableRunner creates a runner, asks for thread count, loads persisted state.
func NewDurableRunner(filePath string, threads int) *DurableRunner {
	dr := &DurableRunner{
		filePath: filePath,
		threads:  threads,
		records:  make(map[int]*DurableRecord),
		taskCh:   make(chan *DurableRecord, 256),
		stopCh:   make(chan struct{}),
		cron:     cron.New(),
	}
	dr.load()
	return dr
}

// Start launches worker threads and re-queues any pending/failed tasks from last run.
func (dr *DurableRunner) Start() {
	for i := 0; i < dr.threads; i++ {
		dr.wg.Add(1)
		go dr.worker(i)
	}
	dr.cron.Start()

	// Re-queue anything that was pending or running (crashed mid-flight)
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

	log.Printf("[DurableRunner] Started with %d threads, file: %s", dr.threads, dr.filePath)
}

// Stop drains the queue and shuts down workers gracefully.
func (dr *DurableRunner) Stop() {
	close(dr.stopCh)
	close(dr.taskCh)
	dr.wg.Wait()
	dr.cron.Stop()
	dr.save()
	log.Println("[DurableRunner] Stopped cleanly")
}

// AssignDurableRun queues a Function to run once, surviving crashes.
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

// AssignScheduled queues a Function to run on a cron schedule (e.g. "*/5 * * * *").
// It will re-run on the schedule until the runner is stopped.
func (dr *DurableRunner) AssignScheduled(f Function, cronExpr string) error {
	rec := &DurableRecord{
		Func:     f,
		Status:   StatusScheduled,
		Schedule: cronExpr,
		AddedAt:  time.Now(),
	}
	dr.mu.Lock()
	dr.records[f.SaveID] = rec
	dr.mu.Unlock()
	dr.save()

	_, err := dr.cron.AddFunc(cronExpr, func() {
		// Clone the record for each run so history is independent
		run := &DurableRecord{
			Func:     f,
			Status:   StatusPending,
			Schedule: cronExpr,
			AddedAt:  time.Now(),
		}
		dr.mu.Lock()
		dr.records[f.SaveID] = run
		dr.mu.Unlock()
		dr.taskCh <- run
	})
	if err != nil {
		return fmt.Errorf("invalid cron expression %q: %w", cronExpr, err)
	}

	log.Printf("[DurableRunner] Scheduled function %d with cron: %s", f.SaveID, cronExpr)
	return nil
}

// ── Status ────────────────────────────────────────────────────────────────────

// Status returns the current state of a function by its SaveID.
func (dr *DurableRunner) Status(id int) (DurableRecord, bool) {
	dr.mu.Lock()
	defer dr.mu.Unlock()
	rec, ok := dr.records[id]
	if !ok {
		return DurableRecord{}, false
	}
	return *rec, true
}

// StatusAll returns a snapshot of every tracked function.
func (dr *DurableRunner) StatusAll() []DurableRecord {
	dr.mu.Lock()
	defer dr.mu.Unlock()
	out := make([]DurableRecord, 0, len(dr.records))
	for _, rec := range dr.records {
		out = append(out, *rec)
	}
	return out
}

// CrashReport returns all records that crashed or failed.
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

// PrintCrashReport pretty-prints the crash report to stdout.
func (dr *DurableRunner) PrintCrashReport() {
	report := dr.CrashReport()
	if len(report) == 0 {
		fmt.Println("[CrashReport] No crashes recorded.")
		return
	}
	fmt.Printf("[CrashReport] %d crash(es) found:\n", len(report))
	for _, rec := range report {
		fmt.Printf("  ID: %d | Func: %s | Status: %s | Error: %s | CrashLog: %s\n",
			rec.Func.SaveID, rec.Func.Fun, rec.Status, rec.Error, rec.CrashLog)
	}
}

func (dr *DurableRunner) worker(id int) {
	defer dr.wg.Done()
	log.Printf("[Worker %d] Ready", id)

	for rec := range dr.taskCh {
		dr.execute(id, rec)
	}

	log.Printf("[Worker %d] Shut down", id)
}

func (dr *DurableRunner) execute(workerID int, rec *DurableRecord) {
	now := time.Now()

	dr.mu.Lock()
	rec.Status = StatusRunning
	rec.StartedAt = &now
	dr.mu.Unlock()
	dr.save()

	log.Printf("[Worker %d] Running function %d (%s)", workerID, rec.Func.SaveID, rec.Func.Fun)

	// Catch panics and log them as crash reports
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
		// Only mark done and remove from persistence if it's a one-shot task
		if rec.Schedule == "" {
			rec.Status = StatusDone
			log.Printf("[Worker %d] Function %d done", workerID, rec.Func.SaveID)
		} else {
			rec.Status = StatusScheduled
		}
	}
	dr.mu.Unlock()
	dr.save()
}

type persistedStore struct {
	Records map[int]*DurableRecord `json:"records"`
}

func (dr *DurableRunner) save() {
	dr.mu.Lock()
	defer dr.mu.Unlock()

	// Only persist non-done records so the file doesn't grow forever
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

func (dr *DurableRunner) load() {
	data, err := os.ReadFile(dr.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return // fresh start
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
