//go:build !nosqlite
// +build !nosqlite

package storage

func NewDefaultPieceCompletionForDir(dir string) (PieceCompletion, error) {
	return NewSqlitePieceCompletion(dir)
}
