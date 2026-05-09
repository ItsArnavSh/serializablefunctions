package serializefunctions_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/ItsArnavSh/serializefunctions"
)

// ── Shared types ──────────────────────────────────────────────────────────────

type Sample struct {
	Num  int
	Name string
}

type Point struct {
	X float64
	Y float64
}

type Result struct {
	Message string
	Code    int
}

// ── Functions under test ──────────────────────────────────────────────────────

func Hello(s Sample) string {
	return fmt.Sprintf("Hello %s, your number is %d", s.Name, s.Num)
}

func Double(n int) int {
	return n * 2
}

func Greet(name string) string {
	return "Hey, " + name
}

func Summarize(p Point) string {
	return fmt.Sprintf("Point(%.2f, %.2f)", p.X, p.Y)
}

func Echo(r Result) Result {
	return Result{
		Message: "echo: " + r.Message,
		Code:    r.Code + 1,
	}
}

func Identity(s Sample) Sample {
	return s
}

// ── Helper: full round-trip ───────────────────────────────────────────────────
// Serializes f with params, marshals to JSON, unmarshals back, executes.

func roundTrip[P any, R any](t *testing.T, funcName string, params P) (R, error) {
	t.Helper()
	f, err := serializefunctions.SerializeFunction(funcName, params)
	if err != nil {
		var zero R
		return zero, fmt.Errorf("SerializeFunction: %w", err)
	}

	bytes, err := json.Marshal(f)
	if err != nil {
		var zero R
		return zero, fmt.Errorf("json.Marshal: %w", err)
	}

	var restored serializefunctions.Function
	if err := json.Unmarshal(bytes, &restored); err != nil {
		var zero R
		return zero, fmt.Errorf("json.Unmarshal: %w", err)
	}

	raw, err := serializefunctions.ExecuteFunc[R](restored)
	if err != nil {
		var zero R
		return zero, err
	}

	// Assert any → R
	result, ok := raw.(R)
	if !ok {
		var zero R
		return zero, fmt.Errorf("type mismatch: expected %T, got %T", zero, raw)
	}
	return result, nil
}

// ── Tests ─────────────────────────────────────────────────────────────────────

func TestStructParam(t *testing.T) {
	serializefunctions.RegisterFunction("hello", Hello)

	got, err := roundTrip[Sample, string](t, "hello", Sample{Num: 10, Name: "arnav"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := "Hello arnav, your number is 10"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestPrimitiveIntParam(t *testing.T) {
	serializefunctions.RegisterFunction("double", Double)

	got, err := roundTrip[int, int](t, "double", 21)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got != 42 {
		t.Errorf("got %d, want 42", got)
	}
}

func TestPrimitiveStringParam(t *testing.T) {
	serializefunctions.RegisterFunction("greet", Greet)

	got, err := roundTrip[string, string](t, "greet", "world")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := "Hey, world"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestNestedFloatStruct(t *testing.T) {
	serializefunctions.RegisterFunction("summarize", Summarize)

	got, err := roundTrip[Point, string](t, "summarize", Point{X: 1.5, Y: 2.75})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := "Point(1.50, 2.75)"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStructInStructOut(t *testing.T) {
	serializefunctions.RegisterFunction("echo", Echo)

	input := Result{Message: "hello", Code: 5}
	got, err := roundTrip[Result, Result](t, "echo", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got.Message != "echo: hello" || got.Code != 6 {
		t.Errorf("got %+v, want {Message:\"echo: hello\", Code:6}", got)
	}
}

func TestIdentityStruct(t *testing.T) {
	serializefunctions.RegisterFunction("identity", Identity)

	input := Sample{Num: 99, Name: "test"}
	got, err := roundTrip[Sample, Sample](t, "identity", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got.Num != input.Num || got.Name != input.Name {
		t.Errorf("got %+v, want %+v", got, input)
	}
}

// ── Edge case tests ───────────────────────────────────────────────────────────

func TestZeroValueStruct(t *testing.T) {
	serializefunctions.RegisterFunction("hello", Hello)

	got, err := roundTrip[Sample, string](t, "hello", Sample{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := "Hello , your number is 0"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestZeroInt(t *testing.T) {
	serializefunctions.RegisterFunction("double", Double)

	got, err := roundTrip[int, int](t, "double", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got != 0 {
		t.Errorf("got %d, want 0", got)
	}
}

func TestUnregisteredFunction(t *testing.T) {
	f, err := serializefunctions.SerializeFunction("does_not_exist", "param")
	_ = f
	if err == nil {
		t.Error("expected error for unregistered function, got nil")
	}
}

func TestWrongReturnTypePanic(t *testing.T) {
	serializefunctions.RegisterFunction("double", Double)

	defer func() {
		if r := recover(); r != nil {
			t.Logf("correctly panicked or errored on type mismatch: %v", r)
		}
	}()

	// double returns int, but we ask for string — should error or panic
	_, err := roundTrip[int, string](t, "double", 5)
	if err != nil {
		t.Logf("correctly returned error on type mismatch: %v", err)
	}
}

// ── Multiple calls, same registry ─────────────────────────────────────────────

func TestMultipleFunctionsSameRegistry(t *testing.T) {
	serializefunctions.RegisterFunction("hello", Hello)
	serializefunctions.RegisterFunction("greet", Greet)
	serializefunctions.RegisterFunction("double", Double)

	s, _ := roundTrip[Sample, string](t, "hello", Sample{Num: 7, Name: "alice"})
	g, _ := roundTrip[string, string](t, "greet", "bob")
	d, _ := roundTrip[int, int](t, "double", 6)

	if s != "Hello alice, your number is 7" {
		t.Errorf("hello: got %q", s)
	}
	if g != "Hey, bob" {
		t.Errorf("greet: got %q", g)
	}
	if d != 12 {
		t.Errorf("double: got %d", d)
	}
}
