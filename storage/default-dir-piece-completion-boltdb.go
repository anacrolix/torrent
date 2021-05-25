//go:build !cgo && !noboltdb
// +build !cgo,!noboltdb

package storage

func NewDefaultPieceCompletionForDir(dir string) (PieceCompletion, error) {
	return NewBoltPieceCompletion(dir)
}
