package torrent

// Sorts false before true.
func compareBool(a, b bool) int {
	if a == b {
		return 0
	}
	if b {
		return -1
	}
	return 1
}
