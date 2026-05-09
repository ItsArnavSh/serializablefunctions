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

type FuncStatus string

const (
	StatusPending FuncStatus = "pending"
	StatusRunning FuncStatus = "running"
	StatusDone    FuncStatus = "done"
	StatusFailed  FuncStatus = "failed"
)

// ── Persisted record ──────────────────────────────────────────────────────────

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

type DurableRunner struct {
	filePath string
	threads  int

	mu      sync.Mutex
	records map[int]*DurableRecord

	taskCh chan *DurableRecord
	wg     sync.WaitGroup
}

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

func (dr *DurableRunner) Start() {
	for i := 0; i < dr.threads; i++ {
		dr.wg.Add(1)
		go dr.worker(i)
	}

	// Re-queue anything pending or mid-flight when the process last died
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

func (dr *DurableRunner) Stop() {
	close(dr.taskCh)
	dr.wg.Wait()
	dr.save()
	log.Println("[DurableRunner] Stopped cleanly")
}

// ── Assign ────────────────────────────────────────────────────────────────────

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

// ── Status ────────────────────────────────────────────────────────────────────

func (dr *DurableRunner) Status(id int) (DurableRecord, bool) {
	dr.mu.Lock()
	defer dr.mu.Unlock()
	rec, ok := dr.records[id]
	if !ok {
		return DurableRecord{}, false
	}
	return *rec, true
}

func (dr *DurableRunner) StatusAll() []DurableRecord {
	dr.mu.Lock()
	defer dr.mu.Unlock()
	out := make([]DurableRecord, 0, len(dr.records))
	for _, rec := range dr.records {
		out = append(out, *rec)
	}
	return out
}

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

// ── Worker ────────────────────────────────────────────────────────────────────

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

func (dr *DurableRunner) save() {
	dr.mu.Lock()
	defer dr.mu.Unlock()

	// Only persist non-done records — done means finished, no need to track
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
