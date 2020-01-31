package bencode

// Bytes effectively a type alias but adds the marshalling functions.
type Bytes []byte

var (
	_ Unmarshaler = &Bytes{}
	_ Marshaler   = &Bytes{}
	_ Marshaler   = Bytes{}
)

// UnmarshalBencode bencode decode for the provided bytes
func (me *Bytes) UnmarshalBencode(b []byte) error {
	*me = append([]byte(nil), b...)
	return nil
}

// MarshalBencode bencode encoder for the current bytes
func (me Bytes) MarshalBencode() ([]byte, error) {
	return me, nil
}
