// Bolt piece completion is not available, and neither is sqlite.
//go:build (!cgo || nosqlite) && (noboltdb || wasm)
// +build !cgo nosqlite
// +build noboltdb wasm

package storage

import (
	"errors"
)

func NewDefaultPieceCompletionForDir(dir string) (PieceCompletion, error) {
	return nil, errors.New("y ur OS no have features")
}
