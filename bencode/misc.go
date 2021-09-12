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
	if len(b) == 0 {
		return ""
	}
	// See https://github.com/golang/go/issues/40701.
	var s string
	hdr := (*reflect.StringHeader)(unsafe.Pointer(&s))
	hdr.Data = uintptr(unsafe.Pointer(&b[0]))
	hdr.Len = len(b)
	return s
}
