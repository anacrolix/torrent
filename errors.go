package torrent

// I don't trust errors.New with allocations, and I know I can use unique.Handle if I get desperate.
type stringError string

func (e stringError) Error() string {
	return string(e)
}
