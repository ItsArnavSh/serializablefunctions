package main

import (
	"fmt"

	"github.com/ItsArnavSh/serializefunctions"
)

type Sample struct {
	Num  int
	Name string
}

func Hello(word Sample) string {
	return fmt.Sprintf("%s %d", word.Name, word.Num)
}

func main() {
	// Setup
	runner := serializefunctions.NewDurableRunner("tasks.json", 4)
	serializefunctions.RegisterFunction("hello", Hello)
	runner.Start()
	defer runner.Stop()

	// One-shot durable task
	f, _ := serializefunctions.SerializeFunction("hello", Sample{Name: "arnav"})
	runner.AssignDurableRun(f)

	// Scheduled task — runs every minute
	//f2, _ := serializefunctions.SerializeFunction("hello", Sample{Name: "cron"})
	//runner.AssignScheduled(f2, "* * * * *")

	// Check status by ID
	rec, _ := runner.Status(f.SaveID)
	fmt.Println(rec.Status) // pending / running / done / failed

	// All statuses
	runner.StatusAll()
	// Crash report
	runner.PrintCrashReport()

}
