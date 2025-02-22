//go:build !windows

package dht

func ignoreReadFromError(error) bool {
	// Good unix.
	return false
}
