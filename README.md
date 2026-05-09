# serializablefunctions

Lets you pack up function calls along with their parameters, marshal them, store them in a database, and run them later. Surviving crashes, restarts, and whatever else goes wrong.

```go
// Pack a function call
f, _ := serializefunctions.SerializeFunction("sendEmail", EmailParams{To: "user@example.com"})

// f is now a plain struct, marshal it, store it, send it over the wire
bytes, _ := json.Marshal(f)

// Later, on any restart, in any worker
var restored serializefunctions.Function
json.Unmarshal(bytes, &restored)
result, _ := serializefunctions.ExecuteFunc[string](restored)
```

## Install

```bash
go get github.com/ItsArnavSh/serializablefunctions
```

## Quickstart

```go
// 1. Define your function, one param in, one value out
func Hello(s Sample) string {
    return fmt.Sprintf("Hello %s, your number is %d", s.Name, s.Num)
}

// 2. Register it once at startup
serializefunctions.RegisterFunction("hello", Hello)

// 3. Serialize a call with its params
f, _ := serializefunctions.SerializeFunction("hello", Sample{Num: 10, Name: "arnav"})

// 4. Marshal → store → restore (survives crashes, DB round-trips, whatever)
bytes, _ := json.Marshal(f)
var restored serializefunctions.Function
json.Unmarshal(bytes, &restored)

// 5. Execute and get the result
ans, _ := serializefunctions.ExecuteFunc[string](restored)
fmt.Println(ans) // Hello arnav, your number is 10
```

## Durable Execution

For fire-and-forget jobs that must complete even across crashes, use `DurableRunner`. It persists pending tasks to a JSON file and re-queues anything that was mid-flight when the process died.

```go
serializefunctions.RegisterFunction("processOrder", ProcessOrder)

runner := serializefunctions.NewDurableRunner("tasks.json", 4) // 4 worker threads
runner.Start()
defer runner.Stop()

f, _ := serializefunctions.SerializeFunction("processOrder", Order{ID: 42})
runner.AssignDurableRun(f)

// Kill the process here: on next restart, the task will be picked up and run
```

Workers use a demand pattern: they pull tasks from a channel themselves rather than being pushed to, so there's no dispatcher bottleneck.

### Checking Status

Every `Function` has a `SaveID` field: use it to query state at any time.

```go
rec, ok := runner.Status(f.SaveID)
fmt.Println(rec.Status) // pending | running | done | failed

// All tasks
all := runner.StatusAll()

// Crash report
runner.PrintCrashReport()
```

### Crash Recovery

On startup, `runner.Start()` scans the persisted file. Any task still marked `pending` or `running` was interrupted, it gets re-queued automatically with a `CrashLog` entry explaining what happened.

Tasks are only removed from the file once they reach `done`. Failed tasks stay in the file and show up in the crash report.

## Limitations

Functions must have exactly **one input parameter and one return value**. If your function has multiple arguments, wrap it:

```go
// Your real function
func SendEmail(to string, subject string, body string) error { ... }

// Wrapper for serialization
type EmailParams struct {
    To      string
    Subject string
    Body    string
}

func SendEmailWrapped(p EmailParams) string {
    err := SendEmail(p.To, p.Subject, p.Body)
    if err != nil {
        return err.Error()
    }
    return "ok"
}

serializefunctions.RegisterFunction("sendEmail", SendEmailWrapped)
```
