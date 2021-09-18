package bencode

type Bytes []byte

var (
	_ Unmarshaler = (*Bytes)(nil)
	_ Marshaler   = (*Bytes)(nil)
	_ Marshaler   = Bytes{}
)

func (me *Bytes) UnmarshalBencode(b []byte) error {
	*me = append([]byte(nil), b...)
	return nil
}

func (me Bytes) MarshalBencode() ([]byte, error) {
	return me, nil
}
