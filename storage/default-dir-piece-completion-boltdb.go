// Bolt piece completion is available, and sqlite is not.
//go:build !noboltdb && (!cgo || nosqlite)
// +build !noboltdb
// +build !cgo nosqlite

package storage

func NewDefaultPieceCompletionForDir(dir string) (PieceCompletion, error) {
	return NewBoltPieceCompletion(dir)
}
