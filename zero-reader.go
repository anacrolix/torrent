package torrent

// TODO: This should implement extra methods to make io.CopyN more efficient.
var zeroReader zeroReaderType

type zeroReaderType struct{}

func (me zeroReaderType) Read(b []byte) (n int, err error) {
	clear(b)
	n = len(b)
	return
}
