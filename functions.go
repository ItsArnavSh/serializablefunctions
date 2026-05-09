package serializefunctions

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
)

var func_table = make(map[string]funcRuntime)

type Function struct {
	Fun       string
	Is_online bool
	Params    []byte
	SaveID    int
}
type funcRuntime struct {
	Params   reflect.Type
	RetType  reflect.Type
	Function func(any) any
	Online   bool
}

func RegisterFunction[T, U any](func_name string, f func(param T) U) {

	paramType := reflect.TypeFor[T]()
	retType := reflect.TypeFor[U]()

	func_table[func_name] = funcRuntime{
		Function: adapt(f),
		Params:   paramType,
		RetType:  retType,
	}
}
func adapt[T any, U any](fn func(T) U) func(any) any {
	return func(v any) any {
		return fn(v.(T))
	}
}
func SerializeFunction[T any](f string, params T) (Function, error) {
	function, ok := func_table[f]
	if !ok {
		return Function{}, fmt.Errorf("Function does not exist")
	}
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

func ExecuteFunc[T any](f Function) (any, error) {
	typ := func_table[f.Fun].Params
	ptr := reflect.New(typ)
	var zero T
	err := json.Unmarshal(f.Params, ptr.Interface())
	if err != nil {
		return zero, err
	}
	params := ptr.Elem().Interface()
	ret := func_table[f.Fun].Function(params)
	return ret, nil
}
