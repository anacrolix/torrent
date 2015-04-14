package torrent

// The public interface for a torrent within a Client.

// A handle to a live torrent within a Client.
type Torrent struct {
	cl *Client
	*torrent
}

func (t *Torrent) NewReader() (ret *Reader) {
	ret = &Reader{
		t:         t,
		readahead: 5 * 1024 * 1024,
	}
	return
}
