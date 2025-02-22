package krpc

import (
	"testing"

	"github.com/anacrolix/torrent/bencode"
	"github.com/stretchr/testify/assert"
)

func TestMarshalID(t *testing.T) {
	var id ID
	copy(id[:], []byte("012345678901234567890"))
	assert.Equal(t, "20:01234567890123456789", string(bencode.MustMarshal(id)))
}
