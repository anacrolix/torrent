package bencode

type Bytes []byte

var (
	_ Unmarshaler = &Bytes{}
	_ Marshaler   = &Bytes{}
	_ Marshaler   = Bytes{}
)

func (me *Bytes) UnmarshalBencode(b []byte) (ret error) {
	*me = make([]byte, len(b))
	copy(*me, b)
	return
}

func (me Bytes) MarshalBencode() ([]byte, error) {
	return me, nil
}
