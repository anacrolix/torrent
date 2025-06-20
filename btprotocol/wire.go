package btprotocol

import (
	"encoding"
	"io"
)

func Write(w io.Writer, set ...encoding.BinaryMarshaler) (n int, err error) {
	for _, s := range set {
		encoded, err := s.MarshalBinary()
		if err != nil {
			return n, err
		}

		_n, err := w.Write(encoded)
		if err != nil {
			return n + _n, err
		}
		n += _n
	}

	return n, nil
}
