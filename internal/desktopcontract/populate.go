package desktopcontract

import (
	"reflect"
	"time"
)

// populatedTime has a fraction and a non-UTC zone on purpose, so a type that writes it unchanged
// breaks the Time rule instead of passing by luck.
var populatedTime = time.Date(2026, 10, 26, 14, 30, 15, 123456789, time.FixedZone("test", 2*3600))

// Populate sets every exported field of the struct that ptr points to, at any depth, to a non-zero
// value: text, 7, true, a timestamp, one slice element, one map entry. A type encoded after Populate
// therefore carries every key it can ever send, which is what makes the contract table complete.
// It panics on a kind it cannot fill, so a new field type is a loud test failure and not a silent gap.
func Populate(ptr any) {
	fill(reflect.ValueOf(ptr).Elem())
}

func fill(v reflect.Value) {
	if v.Type() == reflect.TypeOf(time.Time{}) {
		v.Set(reflect.ValueOf(populatedTime))
		return
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString("x")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(7)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(7)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(7)
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		fill(v.Elem())
	case reflect.Slice:
		s := reflect.MakeSlice(v.Type(), 1, 1)
		fill(s.Index(0))
		v.Set(s)
	case reflect.Map:
		m := reflect.MakeMap(v.Type())
		key, elem := reflect.New(v.Type().Key()).Elem(), reflect.New(v.Type().Elem()).Elem()
		fill(key)
		fill(elem)
		m.SetMapIndex(key, elem)
		v.Set(m)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				fill(v.Field(i))
			}
		}
	default:
		panic("desktopcontract.Populate cannot fill " + v.Type().String())
	}
}
