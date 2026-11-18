//go:build !linux && !darwin && !windows

package storage

func renameNoReplaceSys(from, to string) error {
	return renameNoReplaceFallback(from, to)
}
