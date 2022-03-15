// Bolt piece completion is available, and sqlite is not.
//go:build !noboltdb && (!cgo || nosqlite) && !wasm
// +build !noboltdb
// +build !cgo nosqlite
// +build !wasm

package storage

func NewDefaultPieceCompletionForDir(dir string) (PieceCompletion, error) {
	return NewBoltPieceCompletion(dir)
}
