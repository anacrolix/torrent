package torrent

import (
	"fmt"
	"net"
	"testing"

	"github.com/bradfitz/iter"
	"github.com/james-lawrence/torrent/bencode"
	pp "github.com/james-lawrence/torrent/btprotocol"
	"github.com/james-lawrence/torrent/internal/testutil"
	"github.com/james-lawrence/torrent/metainfo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Check the given Request is correct for various torrent offsets.
func TestTorrentRequest(t *testing.T) {
	const s = 472183431 // Length of torrent.
	for _, _case := range []struct {
		off int64   // An offset into the torrent.
		req request // The expected Request. The zero value means !ok.
	}{
		// Invalid offset.
		{-1, request{}},
		{0, newRequest(0, 0, 16384)},
		// One before the end of a piece.
		{1<<18 - 1, newRequest(0, 1<<18-16384, 16384)},
		// Offset beyond torrent length.
		{472 * 1 << 20, request{}},
		// One before the end of the torrent. Complicates the chunk length.
		{s - 1, newRequest((s-1)/(1<<18), (s-1)%(1<<18)/(16384)*(16384), 12935)},
		{1, newRequest(0, 0, 16384)},
		// One before end of chunk.
		{16383, newRequest(0, 0, 16384)},
		// Second chunk.
		{16384, newRequest(0, 16384, 16384)},
	} {
		req, ok := torrentOffsetRequest(472183431, 1<<18, 16384, _case.off)
		if (_case.req == request{}) == ok {
			t.Fatalf("expected %v, got %v", _case.req, req)
		}
		if req != _case.req {
			t.Fatalf("expected %v, got %v", _case.req, req)
		}
	}
}

func TestTorrentString(t *testing.T) {
	tor := &torrent{}
	s := tor.InfoHash().HexString()
	if s != "0000000000000000000000000000000000000000" {
		t.FailNow()
	}
}

// This benchmark is from the observation that a lot of overlapping Readers on
// a large torrent with small pieces had a lot of overhead in recalculating
// piece priorities everytime a reader (possibly in another Torrent) changed.
func BenchmarkUpdatePiecePriorities(b *testing.B) {
	const (
		numPieces   = 13410
		pieceLength = 256 << 10
	)
	cl := &Client{config: &ClientConfig{}}
	ts, err := New(metainfo.Hash{})
	require.NoError(b, err)
	t := cl.newTorrent(ts)
	require.NoError(b, t.setInfo(&metainfo.Info{
		Pieces:      make([]byte, metainfo.HashSize*numPieces),
		PieceLength: pieceLength,
		Length:      pieceLength * numPieces,
	}))
	assert.EqualValues(b, 13410, t.numPieces())
	for range iter.N(7) {
		r := t.NewReader()
		r.SetReadahead(32 << 20)
		r.Seek(3500000, 0)
	}
	assert.Len(b, t.readers, 7)
	for i := 0; i < int(t.numPieces()); i += 3 {
		t.chunks.Complete(i)
	}
	t.DownloadPieces(0, t.numPieces())
	for range iter.N(b.N) {
		t.updateAllPiecePriorities()
	}
}

func TestPieceHashFailed(t *testing.T) {
	mi := testutil.GreetingMetaInfo()
	cl := new(Client)
	cl.config = TestingConfig(t)
	ts, err := NewFromMetaInfo(mi, OptionStorage(testutil.NewBadStorage()))
	require.NoError(t, err)
	tt := cl.newTorrent(ts)
	tt.setChunkSize(2)
	require.NoError(t, tt.setInfoBytes(mi.InfoBytes))
	tt.lock()
	tt.chunks.Validate(1)
	require.True(t, tt.chunks.ChunksAvailable(1))
	tt.pieceHashed(1, fmt.Errorf("boom"))

	// the piece should be marked as a failure. this means the connections will
	// retry the piece either during their write loop or during their cleanup phase.
	require.True(t, tt.chunks.Failed(tt.chunks.failed).Contains(5))
	tt.unlock()
}

// Check the behaviour of Torrent.Metainfo when metadata is not completed.
func TestTorrentMetainfoIncompleteMetadata(t *testing.T) {
	cfg := TestingConfig(t)
	// cfg.HeaderObfuscationPolicy = HeaderObfuscationPolicy{
	// 	Preferred:        false,
	// 	RequirePreferred: false,
	// }
	// cfg.Debug = log.New(os.Stderr, "[debug] ", log.Flags())
	cl, err := Autosocket(t).Bind(NewClient(cfg))
	require.NoError(t, err)
	defer cl.Close()

	mi := testutil.GreetingMetaInfo()
	ih := mi.HashInfoBytes()
	ts, err := New(ih)
	require.NoError(t, err)
	tt, _, _ := cl.Start(ts)
	assert.Nil(t, tt.(*torrent).Metainfo().InfoBytes)
	assert.False(t, tt.(*torrent).haveAllMetadataPieces())

	nc, err := net.Dial("tcp", fmt.Sprintf(":%d", cl.LocalPort()))
	require.NoError(t, err)
	defer nc.Close()

	var pex PeerExtensionBits
	pex.SetBit(pp.ExtensionBitExtended)

	ebits, info, err := pp.Handshake{
		Bits:   pex,
		PeerID: [20]byte{},
	}.Outgoing(nc, ih)

	require.NoError(t, err)
	assert.True(t, ebits.GetBit(pp.ExtensionBitExtended))
	assert.EqualValues(t, cl.PeerID(), info.PeerID)
	assert.EqualValues(t, ih, info.Hash)
	assert.EqualValues(t, 0, tt.(*torrent).metadataSize())

	func() {
		tt.(*torrent).lock()
		defer tt.(*torrent).unlock()
		go func() {
			_, err = nc.Write(pp.Message{
				Type:       pp.Extended,
				ExtendedID: pp.HandshakeExtendedID,
				ExtendedPayload: func() []byte {
					d := map[string]interface{}{
						"metadata_size": len(mi.InfoBytes),
					}
					b, err := bencode.Marshal(d)
					if err != nil {
						panic(err)
					}
					return b
				}(),
			}.MustMarshalBinary())
			require.NoError(t, err)
		}()
		tt.(*torrent).metadataChanged.Wait()
	}()
	assert.Equal(t, make([]byte, len(mi.InfoBytes)), tt.(*torrent).metadataBytes)
	assert.False(t, tt.(*torrent).haveAllMetadataPieces())
	assert.Nil(t, tt.(*torrent).Metainfo().InfoBytes)
}
