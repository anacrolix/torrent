package util

import (
	"fmt"
	"reflect"
)

func CopyExact(dest interface{}, src interface{}) {
	dV := reflect.ValueOf(dest)
	sV := reflect.ValueOf(src)
	if dV.Kind() == reflect.Ptr {
		dV = dV.Elem()
	}
	if dV.Kind() == reflect.Array && !dV.CanAddr() {
		panic(fmt.Sprintf("dest not addressable: %T", dest))
	}
	if sV.Kind() == reflect.String {
		sV = sV.Convert(reflect.SliceOf(dV.Type().Elem()))
	}
	if dV.Len() != sV.Len() {
		panic(fmt.Sprintf("dest len (%d) != src len (%d)", dV.Len(), sV.Len()))
	}
	if dV.Len() != reflect.Copy(dV, sV) {
		panic("dammit")
	}
}
