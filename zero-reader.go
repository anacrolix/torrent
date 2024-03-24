package torrent

var zeroReader zeroReaderType

type zeroReaderType struct{}

func (me zeroReaderType) Read(b []byte) (n int, err error) {
	clear(b)
	n = len(b)
	return
}
