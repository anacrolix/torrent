//go:build !go1.20

package bencode

import "unsafe"

func bytesAsString(b []byte) string {
	return *(*string)(unsafe.Pointer(&b))
}
