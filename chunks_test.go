package torrent

import (
	"os"
	"testing"
	"time"

	"github.com/RoaringBitmap/roaring"
	"github.com/davecgh/go-spew/spew"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/anacrolix/torrent/internal/testutil"
	"github.com/anacrolix/torrent/metainfo"
)

var _ = spew.Sdump

// returns a 16KiB torrent with 1 KiB pieces.
func tinyTorrentInfo() *metainfo.Info {
	return &metainfo.Info{
		Length:      int64(16 * 1 << 10),
		PieceLength: int64(1 << 10),
	}
}

func filledbmap(n int) *roaring.Bitmap {
	available := roaring.NewBitmap()
	available.AddRange(0, uint64(n))
	return available
}

func fromFile(path string) (info metainfo.Info, err error) {
	var (
		mi *metainfo.MetaInfo
	)

	if mi, err = metainfo.LoadFromFile(path); err != nil {
		return info, err
	}

	if info, err = mi.UnmarshalInfo(); err != nil {
		return info, err
	}

	return info, nil
}

func quickpopulate(p *chunks) *chunks {
	for i := int64(0); i < p.cmaximum; i++ {
		p.missing.Set(int(i), int(i))
	}
	return p
}

func smallpopulate(p *chunks) *chunks {
	for i := int64(0); i < 10; i++ {
		p.missing.Set(int(i), int(i))
	}
	return p
}

func BenchmarkChunksPop(b *testing.B) {
	info, err := fromFile("testdata/bootstrap.dat.torrent")
	require.NoError(b, err)
	p := quickpopulate(newChunks(defaultChunk, &info))

	n := p.missing.Len()
	available := filledbmap(n)

	for i := 0; i < b.N && i < n; i++ {
		_, err := p.Pop(1, available)
		require.NoError(b, err)
	}
}

func TestNumChunks(t *testing.T) {
	// common denominators
	// 32 KiB, 8 KiB, 1 KiB
	assert.Equal(t, int64(32), numChunks(32*(1<<10), 8*1<<10, 1<<10))
	// 32 KiB, 8 KiB, 2 KiB
	assert.Equal(t, int64(16), numChunks(32*(1<<10), 8*1<<10, 2*1<<10))
	// 32 KiB, 8 KiB, 4 KiB
	assert.Equal(t, int64(8), numChunks(32*(1<<10), 8*1<<10, 4*1<<10))
	// 32 KiB, 8 KiB, 8 KiB
	assert.Equal(t, int64(4), numChunks(32*1<<10, 8*1<<10, 8*1<<10))
	// 32 KiB, 8 KiB, 16 KiB, when chunksize > piece size we get 1 chunk per piece
	assert.Equal(t, int64(4), numChunks(32*1<<10, 8*1<<10, 16*1<<10))

	// no common denominators
	// 32 KiB, 8 KiB, 3 KiB
	assert.Equal(t, int64(12), numChunks(32*1<<10, 8*1<<10, 3*1<<10))
	// 33 KiB, 8 KiB, 3 KiB
	assert.Equal(t, int64(13), numChunks(33*1<<10, 8*1<<10, 3*1<<10))
	// 34 KiB, 8 KiB, 3 KiB
	assert.Equal(t, int64(13), numChunks(34*1<<10, 8*1<<10, 3*1<<10))
	// 35 KiB, 8 KiB, 3 KiB
	assert.Equal(t, int64(13), numChunks(35*1<<10, 8*1<<10, 3*1<<10))
	// 36 KiB, 8 KiB, 3 KiB
	assert.Equal(t, int64(14), numChunks(36*1<<10, 8*1<<10, 3*1<<10))
	// 37 KiB, 8 KiB, 3 KiB
	assert.Equal(t, int64(14), numChunks(37*1<<10, 8*1<<10, 3*1<<10))
	// 38 KiB, 8 KiB, 3 KiB
	assert.Equal(t, int64(14), numChunks(38*1<<10, 8*1<<10, 3*1<<10))
	// 39 KiB, 8 KiB, 3 KiB
	assert.Equal(t, int64(15), numChunks(39*1<<10, 8*1<<10, 3*1<<10))
	// 40 KiB, 8 KiB, 3 KiB
	assert.Equal(t, int64(15), numChunks(40*1<<10, 8*1<<10, 3*1<<10))
	// 41 KiB, 8 KiB, 3 KiB
	assert.Equal(t, int64(16), numChunks(41*1<<10, 8*1<<10, 3*1<<10))
	// 42 KiB, 8 KiB, 3 KiB
	assert.Equal(t, int64(16), numChunks(42*1<<10, 8*1<<10, 3*1<<10))
	// 43 KiB, 8 KiB, 3 KiB
	assert.Equal(t, int64(16), numChunks(43*1<<10, 8*1<<10, 3*1<<10))
	// 44 KiB, 8 KiB, 3 KiB
	assert.Equal(t, int64(17), numChunks(44*1<<10, 8*1<<10, 3*1<<10))
	// 45 KiB, 8 KiB, 3 KiB
	assert.Equal(t, int64(17), numChunks(45*1<<10, 8*1<<10, 3*1<<10))
	// 46 KiB, 8 KiB, 3 KiB
	assert.Equal(t, int64(17), numChunks(46*1<<10, 8*1<<10, 3*1<<10))
	// 47 KiB, 8 KiB, 3 KiB
	assert.Equal(t, int64(18), numChunks(47*1<<10, 8*1<<10, 3*1<<10))
	// 48 KiB, 8 KiB, 3 KiB
	assert.Equal(t, int64(18), numChunks(48*1<<10, 8*1<<10, 3*1<<10))
}

func TestChunkOffset(t *testing.T) {
	// common denominators
	// 32 KiB, 8 KiB, 1 KiB
	assert.Equal(t, int64(0*1<<10), chunkOffset(0, 0, 8*1<<10, 1<<10))
	assert.Equal(t, int64(1*1<<10), chunkOffset(0, 1, 8*1<<10, 1<<10))
	assert.Equal(t, int64(2*1<<10), chunkOffset(0, 2, 8*1<<10, 1<<10))
	assert.Equal(t, int64(3*1<<10), chunkOffset(0, 3, 8*1<<10, 1<<10))
	assert.Equal(t, int64(4*1<<10), chunkOffset(0, 4, 8*1<<10, 1<<10))
	assert.Equal(t, int64(5*1<<10), chunkOffset(0, 5, 8*1<<10, 1<<10))
	assert.Equal(t, int64(6*1<<10), chunkOffset(0, 6, 8*1<<10, 1<<10))
	assert.Equal(t, int64(7*1<<10), chunkOffset(0, 7, 8*1<<10, 1<<10))
	assert.Equal(t, int64(0*1<<10), chunkOffset(1, 0, 8*1<<10, 1<<10))
	assert.Equal(t, int64(1*1<<10), chunkOffset(1, 1, 8*1<<10, 1<<10))
	assert.Equal(t, int64(2*1<<10), chunkOffset(1, 2, 8*1<<10, 1<<10))
	assert.Equal(t, int64(3*1<<10), chunkOffset(1, 3, 8*1<<10, 1<<10))
	assert.Equal(t, int64(4*1<<10), chunkOffset(1, 4, 8*1<<10, 1<<10))
	assert.Equal(t, int64(5*1<<10), chunkOffset(1, 5, 8*1<<10, 1<<10))
	assert.Equal(t, int64(6*1<<10), chunkOffset(1, 6, 8*1<<10, 1<<10))
	assert.Equal(t, int64(7*1<<10), chunkOffset(1, 7, 8*1<<10, 1<<10))
	assert.Equal(t, int64(0*1<<10), chunkOffset(2, 0, 8*1<<10, 1<<10))
	assert.Equal(t, int64(7*1<<10), chunkOffset(3, 7, 8*1<<10, 1<<10))
	// ensure it would capture all of bytes.
	assert.Equal(t, int64(8*1<<10), chunkOffset(3, 7, 8*1<<10, 1<<10)+1<<10)
}

func TestChunkLength(t *testing.T) {
	// clength < plength - Length 13, PLength 5, CLength 2
	// (total, cidx, plength, clength int64, maximum bool)
	assert.Equal(t, int64(2), chunkLength(13, 0, 5, 2, false))
	assert.Equal(t, int64(2), chunkLength(13, 1, 5, 2, false))
	assert.Equal(t, int64(1), chunkLength(13, 2, 5, 2, false))
	assert.Equal(t, int64(2), chunkLength(13, 3, 5, 2, false))
	assert.Equal(t, int64(2), chunkLength(13, 4, 5, 2, false))
	assert.Equal(t, int64(1), chunkLength(13, 5, 5, 2, false))
	assert.Equal(t, int64(2), chunkLength(13, 6, 5, 2, false))
	assert.Equal(t, int64(1), chunkLength(13, 7, 5, 2, true))

	// clength < plength - Length 13, PLength 5, CLength 3
	assert.Equal(t, int64(3), chunkLength(13, 0, 5, 3, false))
	assert.Equal(t, int64(2), chunkLength(13, 1, 5, 3, false))
	assert.Equal(t, int64(3), chunkLength(13, 2, 5, 3, false))
	assert.Equal(t, int64(2), chunkLength(13, 3, 5, 3, false))
	assert.Equal(t, int64(3), chunkLength(13, 4, 5, 3, true))

	// clength == plength - Length 13, PLength 5, CLength 5
	assert.Equal(t, int64(5), chunkLength(13, 0, 5, 5, false))
	assert.Equal(t, int64(5), chunkLength(13, 1, 5, 5, false))
	assert.Equal(t, int64(3), chunkLength(13, 2, 5, 5, true))

	// clength > plength - Length 13, PLength 5, CLength 5
	assert.Equal(t, int64(5), chunkLength(13, 0, 5, 6, false))
	assert.Equal(t, int64(5), chunkLength(13, 1, 5, 6, false))
	assert.Equal(t, int64(3), chunkLength(13, 2, 5, 6, true))

	// clength > plength - Length 13, PLength 5, CLength 5
	assert.Equal(t, int64(5), chunkLength(13, 0, 5, 10, false))
	assert.Equal(t, int64(5), chunkLength(13, 1, 5, 10, false))
	assert.Equal(t, int64(3), chunkLength(13, 2, 5, 10, true))

	assert.Equal(t, int64(16384), chunkLength(687865856, 31, 524288, 16384, false))
	assert.Equal(t, int64(16384), chunkLength(687865856, 41983, 524288, 16384, true))
}

func TestChunksRequests(t *testing.T) {
	greetingTempDir, mi := testutil.GreetingTestTorrent()
	defer os.RemoveAll(greetingTempDir)
	info, err := mi.UnmarshalInfo()
	require.NoError(t, err)
	test := func(expected, r request, err error) {
		expected.Digest = r.Digest
		expected.Reserved = r.Reserved
		assert.NoError(t, err)
		assert.Equal(t, expected, r)
	}

	c := quickpopulate(newChunks(2, &info))
	r, err := c.request(0, 0)
	test(request{Index: 0, chunkSpec: chunkSpec{Begin: 0, Length: 2}}, r, err)
	r, err = c.request(1, 0)
	test(request{Index: 0, chunkSpec: chunkSpec{Begin: 2, Length: 2}}, r, err)
	r, err = c.request(2, 0)
	test(request{Index: 0, chunkSpec: chunkSpec{Begin: 4, Length: 1}}, r, err)
	r, err = c.request(3, 0)
	test(request{Index: 1, chunkSpec: chunkSpec{Begin: 0, Length: 2}}, r, err)
	r, err = c.request(4, 0)
	test(request{Index: 1, chunkSpec: chunkSpec{Begin: 2, Length: 2}}, r, err)
	r, err = c.request(5, 0)
	test(request{Index: 1, chunkSpec: chunkSpec{Begin: 4, Length: 1}}, r, err)
	r, err = c.request(6, 0)
	test(request{Index: 2, chunkSpec: chunkSpec{Begin: 0, Length: 2}}, r, err)
	r, err = c.request(7, 0)
	test(request{Index: 2, chunkSpec: chunkSpec{Begin: 2, Length: 1}}, r, err)

	c = quickpopulate(newChunks(3, &info))
	r, err = c.request(0, 0)
	test(request{Index: 0, chunkSpec: chunkSpec{Begin: 0, Length: 3}}, r, err)
	r, err = c.request(1, 0)
	test(request{Index: 0, chunkSpec: chunkSpec{Begin: 3, Length: 2}}, r, err)
	r, err = c.request(2, 0)
	test(request{Index: 1, chunkSpec: chunkSpec{Begin: 0, Length: 3}}, r, err)
	r, err = c.request(3, 0)
	test(request{Index: 1, chunkSpec: chunkSpec{Begin: 3, Length: 2}}, r, err)
	r, err = c.request(4, 0)
	test(request{Index: 2, chunkSpec: chunkSpec{Begin: 0, Length: 3}}, r, err)
}

func TestChunksVariousCLength(t *testing.T) {
	greetingTempDir, mi := testutil.GreetingTestTorrent()
	defer os.RemoveAll(greetingTempDir)
	info, err := mi.UnmarshalInfo()
	require.NoError(t, err)

	c := quickpopulate(newChunks(1, &info))
	require.Equal(t, 13, c.Missing())

	c = quickpopulate(newChunks(2, &info))
	assert.Equal(t, []int{0, 1, 2}, c.chunks(0))
	assert.Equal(t, []int{3, 4, 5}, c.chunks(1))
	assert.Equal(t, []int{6, 7}, c.chunks(2))
	require.Equal(t, 8, c.Missing())

	c = quickpopulate(newChunks(3, &info))
	assert.Equal(t, []int{0, 1}, c.chunks(0))
	assert.Equal(t, []int{2, 3}, c.chunks(1))
	assert.Equal(t, []int{4}, c.chunks(2))
	require.Equal(t, 5, c.Missing())

	c = quickpopulate(newChunks(4, &info))
	assert.Equal(t, []int{0, 1}, c.chunks(0))
	assert.Equal(t, []int{2, 3}, c.chunks(1))
	assert.Equal(t, []int{4}, c.chunks(2))
	require.Equal(t, 5, c.Missing())

	c = quickpopulate(newChunks(5, &info))
	assert.Equal(t, []int{0}, c.chunks(0))
	assert.Equal(t, []int{1}, c.chunks(1))
	assert.Equal(t, []int{2}, c.chunks(2))
	require.Equal(t, 3, c.Missing())
}

func TestChunksFailed(t *testing.T) {
	greetingTempDir, mi := testutil.GreetingTestTorrent()
	defer os.RemoveAll(greetingTempDir)
	info, err := mi.UnmarshalInfo()
	require.NoError(t, err)

	c := quickpopulate(newChunks(1, &info))
	touched := roaring.NewBitmap()

	require.Equal(t, 13, c.Missing())

	reqs, err := c.Pop(1, everybmap{})
	require.NoError(t, err)
	for _, req := range reqs {
		touched.Add(uint32(req.Index))
		c.ChunksFailed(int(req.Index))
	}

	union := c.Failed(touched)
	assert.Equal(t, uint64(1), union.GetCardinality())
}

func TestChunksPop(t *testing.T) {
	info, err := fromFile("testdata/bootstrap.dat.torrent")
	require.NoError(t, err)
	p := quickpopulate(newChunks(int(info.PieceLength), &info))

	reqs, err := p.Pop(1, everybmap{})
	require.NoError(t, err)
	for _, req := range reqs {
		require.Equal(t, 0, int(req.Index))
		require.Equal(t, true, req.Reserved.Before(time.Now()))
	}

	reqs, err = p.Pop(1, everybmap{})
	require.NoError(t, err)
	for _, req := range reqs {
		require.Equal(t, 1, int(req.Index))
		require.Equal(t, true, req.Reserved.Before(time.Now()))
	}
}

func TestChunksGraceWindow(t *testing.T) {
	info, err := fromFile("testdata/bootstrap.dat.torrent")
	require.NoError(t, err)
	p := smallpopulate(newChunks(defaultChunk, &info))

	// adjust grace period to be negative to force immediate
	// recovering of outstanding requests.
	p.gracePeriod = -1 * time.Second

	total := p.missing.Len()
	for i := 0; i < 10; i++ {
		_, err = p.Pop(1, everybmap{})
		require.NoError(t, err)
		p.reap(0)
		require.Equal(t, total, p.missing.Len())
	}
}

func TestChunksComplete(t *testing.T) {
	p := quickpopulate(newChunks(1<<8, tinyTorrentInfo()))

	// we start out with 64 chunks missing.
	require.Equal(t, 64, p.missing.Len())
	require.True(t, p.ChunksMissing(0))

	available := filledbmap(1)
	for rs, err := p.Pop(1, available); err == nil; rs, err = p.Pop(1, available) {
		for _, r := range rs {
			require.NoError(t, p.Verify(r))
		}
		require.True(t, p.ChunksHashing(0))
	}
	require.False(t, p.ChunksMissing(0))

	// complete the first piece
	require.True(t, p.Complete(0))
	require.False(t, p.ChunksMissing(0))
	require.False(t, p.ChunksHashing(0))
	require.True(t, p.ChunksComplete(0))

	// we finish with 60 chunks missing.
	require.Equal(t, 60, p.missing.Len())
}

func TestChunksAvailable(t *testing.T) {
	p := quickpopulate(newChunks(1<<8, tinyTorrentInfo()))
	require.Equal(t, 64, p.missing.Len())
	require.True(t, p.ChunksAvailable(0))
}

func TestChunksPend(t *testing.T) {
	p := quickpopulate(newChunks(1<<8, tinyTorrentInfo()))
	require.Equal(t, 64, p.missing.Len())
	require.True(t, p.ChunksPend(0))
}

func TestChunksRelease(t *testing.T) {
	p := quickpopulate(newChunks(1<<8, tinyTorrentInfo()))
	require.Equal(t, 64, p.missing.Len())
	require.False(t, p.ChunksRelease(0))
}
