package bencode

import (
	"reflect"
	"unsafe"
)

// Wow Go is retarded.
var (
	marshalerType   = reflect.TypeOf((*Marshaler)(nil)).Elem()
	unmarshalerType = reflect.TypeOf((*Unmarshaler)(nil)).Elem()
)

func bytesAsString(b []byte) string {
	return unsafe.String(unsafe.SliceData(b), len(b))
}
