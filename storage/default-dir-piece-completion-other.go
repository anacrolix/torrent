//go:build wasm
// +build wasm

package storage

import (
	"errors"
)

func NewDefaultPieceCompletionForDir(dir string) (PieceCompletion, error) {
	return nil, errors.New("y ur OS no have features")
}
