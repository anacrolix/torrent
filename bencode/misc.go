package bencode

import (
	"reflect"
)

// Wow Go is retarded.
var (
	marshalerType   = reflect.TypeOf((*Marshaler)(nil)).Elem()
	unmarshalerType = reflect.TypeOf((*Unmarshaler)(nil)).Elem()
)
