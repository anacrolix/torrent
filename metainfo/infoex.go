package metainfo

import "github.com/anacrolix/torrent/bencode"

// A wrapper around Info that exposes the Bytes directly, in case marshalling
// and unmarshalling Info doesn't produce the same bytes.
type InfoEx struct {
	Info
	// Set when unmarshalling, and used when marshalling. Call .UpdateBytes to
	// set it by bencoding Info.
	Bytes []byte
}

var (
	_ bencode.Marshaler   = &InfoEx{}
	_ bencode.Unmarshaler = &InfoEx{}
)

// Marshals .Info, and sets .Bytes with the result.
func (ie *InfoEx) UpdateBytes() {
	var err error
	ie.Bytes, err = bencode.Marshal(&ie.Info)
	if err != nil {
		panic(err)
	}
}

// Returns the SHA1 hash of .Bytes.
func (ie *InfoEx) Hash() Hash {
	return HashBytes(ie.Bytes)
}

func (ie *InfoEx) UnmarshalBencode(data []byte) error {
	ie.Bytes = append([]byte(nil), data...)
	return bencode.Unmarshal(data, &ie.Info)
}

func (ie *InfoEx) MarshalBencode() ([]byte, error) {
	if ie.Bytes == nil {
		ie.UpdateBytes()
	}
	return ie.Bytes, nil
}

func (info *InfoEx) Piece(i int) Piece {
	return Piece{info, i}
}
