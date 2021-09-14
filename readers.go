package torrent

// Helper-type for holding multiple readers
type readers []*reader

// Add a new reader to readers
func (rs *readers) Push(r *reader) {
	rIdx := len(*rs)
	*rs = append(*rs, r)
	r.idx = uint32(rIdx)
}

// Remove reader from readers
func (rs *readers) Delete(r *reader) {
	rs.DeleteAt(r.idx)
}

// Remove reader at index
func (rs *readers) DeleteAt(index uint32) {
	(*rs)[index] = (*rs)[len(*rs)-1]
	(*rs)[len(*rs)-1] = nil
	*rs = (*rs)[:len(*rs)-1]
}
