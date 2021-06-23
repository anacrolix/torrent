//go:build !cgo && !noboltdb && !wasm
// +build !cgo,!noboltdb,!wasm

package storage

func NewDefaultPieceCompletionForDir(dir string) (PieceCompletion, error) {
	return NewBoltPieceCompletion(dir)
}
