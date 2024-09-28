package metainfo

import (
	"testing"

	g "github.com/anacrolix/generics"
	"github.com/stretchr/testify/assert"

	"github.com/anacrolix/torrent/bencode"
)

func TestMarshalInfo(t *testing.T) {
	var info Info
	g.MakeSliceWithLength(&info.Pieces, 0)
	b, err := bencode.Marshal(info)
	assert.NoError(t, err)
	assert.EqualValues(t, "d4:name0:12:piece lengthi0e6:pieces0:e", string(b))
}
