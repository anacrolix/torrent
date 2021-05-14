//go:build !cgo
// +build !cgo

package storage

func NewDefaultPieceCompletionForDir(dir string) (PieceCompletion, error) {
	return NewBoltPieceCompletion(dir)
}
