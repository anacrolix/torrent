package torrent

// It's possible that we either need to use JS-specific way to allow port reuse, or to fall back to
// dialling TCP without forcing the local address to match the listener. If the fallback is
// implemented, then this should probably return an error to trigger it.
func setReusePortSockOpts(fd uintptr) error {
	return nil
}

func setSockNoLinger(fd uintptr) error {
	return nil
}

func setSockIPTOS(fd uintptr, val int) (err error) {
	return nil
}
