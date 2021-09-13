package bencode

import (
	"reflect"
	"unsafe"
)

// Wow Go is retarded.
var marshalerType = reflect.TypeOf(func() *Marshaler {
	var m Marshaler
	return &m
}()).Elem()

// Wow Go is retarded.
var unmarshalerType = reflect.TypeOf(func() *Unmarshaler {
	var i Unmarshaler
	return &i
}()).Elem()

func bytesAsString(b []byte) string {
	return *(*string)(unsafe.Pointer(&b))
}
