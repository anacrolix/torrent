//go:build cgo && !nosqlite
// +build cgo,!nosqlite

package storage

func NewDefaultPieceCompletionForDir(dir string) (PieceCompletion, error) {
	return NewSqlitePieceCompletion(dir)
}
