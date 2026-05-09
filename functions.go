// Package serializefunctions provides a lightweight framework for registering,
// serializing, and executing Go functions with JSON-based parameter passing.
//
// It enables functions to be stored (e.g. in a database or queue) and executed
// later by name, with automatic JSON marshaling/unmarshaling of parameters.
// This is useful for job queues, RPC-like systems, or deferred execution.
package serializefunctions

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
)

var func_table = make(map[string]funcRuntime)

// Function represents a serialized callable with its parameters.
// It can be stored or transmitted and later executed via ExecuteFunc.
type Function struct {
	// Fun is the registered name of the function to call.
	Fun string

	// Is_online indicates whether the function is available for execution.
	// (Currently not used in the provided implementation.)
	Is_online bool

	// Params contains the JSON-encoded function parameters.
	Params []byte

	// SaveID is a random identifier assigned during serialization.
	SaveID int
}

// funcRuntime holds the runtime metadata for a registered function.
type funcRuntime struct {
	Params   reflect.Type
	RetType  reflect.Type
	Function func(any) any
	Online   bool
}

// RegisterFunction registers a function under a given name so it can be
// serialized and executed later.
//
// The function must have exactly one parameter and one return value.
// Generic types T (input) and U (output) are used for type safety at registration time.
//
// Example:
//
//	RegisterFunction("add", func(a int) int { return a + 10 })
func RegisterFunction[T, U any](func_name string, f func(param T) U) {
	paramType := reflect.TypeFor[T]()
	retType := reflect.TypeFor[U]()

	func_table[func_name] = funcRuntime{
		Function: adapt(f),
		Params:   paramType,
		RetType:  retType,
	}
}

// adapt converts a strongly-typed function into a func(any) any for storage.
func adapt[T any, U any](fn func(T) U) func(any) any {
	return func(v any) any {
		return fn(v.(T))
	}
}

// SerializeFunction serializes a function call into a Function struct.
//
// It looks up the function by name, validates the parameter type (with a warning),
// marshals the parameters to JSON, and assigns a random SaveID.
//
// Returns an error if the function is not registered or JSON marshaling fails.
func SerializeFunction[T any](f string, params T) (Function, error) {
	function, ok := func_table[f]
	if !ok {
		return Function{}, fmt.Errorf("Function does not exist")
	}

	// Note: This comparison uses the concrete type of params.
	// It may not catch all mismatches due to how generics are handled.
	if function.Params != reflect.TypeOf(params) {
		fmt.Println("Types do not match")
	}

	json_res, err := json.Marshal(params)
	if err != nil {
		return Function{}, err
	}

	id := rand.Int()

	return Function{
		Fun:    f,
		Params: json_res,
		SaveID: id,
	}, nil
}

// ExecuteFunc deserializes the parameters and executes the registered function.
//
// The generic type T should match the expected input parameter type of the function.
// It returns the function's result as `any` or an error if unmarshaling or execution fails.
func ExecuteFunc[T any](f Function) (any, error) {
	entry, ok := func_table[f.Fun]
	if !ok {
		return nil, fmt.Errorf("function %s not registered", f.Fun)
	}

	typ := entry.Params
	ptr := reflect.New(typ)

	var zero T
	err := json.Unmarshal(f.Params, ptr.Interface())
	if err != nil {
		return zero, err
	}

	params := ptr.Elem().Interface()
	ret := entry.Function(params)
	return ret, nil
}
