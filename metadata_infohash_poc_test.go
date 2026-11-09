package torrent

import (
	"testing"
	"time"

	g "github.com/anacrolix/generics"
	"github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/internal/testutil"
	"github.com/anacrolix/torrent/metainfo"
	infohash_v2 "github.com/anacrolix/torrent/types/infohash-v2"
)

// Info bytes for a different torrent than the greeting: same shape, different name/contents.
func forgedInfoBytes(t *testing.T) []byte {
	evil := testutil.Torrent{
		Name:  "evil",
		Files: []testutil.File{{Data: "not the greeting\n"}},
	}
	mi, _ := evil.Generate(5)
	return mi.InfoBytes
}

func greetingInfoHash() metainfo.Hash {
	return testutil.GreetingMetaInfo().HashInfoBytes()
}

// SetInfoBytes on a torrent known only by v1 infohash must reject bytes that don't hash to it.
func TestPocSetInfoBytesWrongBytesV1Only(t *testing.T) {
	cl, err := NewClient(TestingConfig(t))
	qt.Assert(t, qt.IsNil(err))
	defer cl.Close()
	tt, _ := cl.AddTorrentInfoHash(greetingInfoHash())
	err = tt.SetInfoBytes(forgedInfoBytes(t))
	qt.Check(t, qt.IsNotNil(err))
	qt.Check(t, qt.IsNil(tt.Info()))
}

// Same check, through the exact path the ut_metadata handler uses (gotMetadataExtensionMsg ->
// saveMetadataPiece -> maybeCompleteMetadata).
func TestPocUtMetadataPathWrongBytesV1Only(t *testing.T) {
	cl, err := NewClient(TestingConfig(t))
	qt.Assert(t, qt.IsNil(err))
	defer cl.Close()
	tt, _ := cl.AddTorrentInfoHash(greetingInfoHash())
	b := forgedInfoBytes(t)
	cl.lock()
	qt.Assert(t, qt.IsNil(tt.setMetadataSize(len(b))))
	tt.saveMetadataPiece(0, b)
	err = tt.maybeCompleteMetadata()
	cl.unlock()
	qt.Check(t, qt.IsNotNil(err))
	qt.Check(t, qt.IsNil(tt.Info()))
}

// End-to-end over the wire using only public API: a malicious peer joins the swarm for the
// greeting infohash with forged info bytes and serves them over ut_metadata. The leecher, which
// only knows the v1 infohash (as from a plain magnet link), must not accept them.
func TestPocMaliciousPeerServesForgedMetadata(t *testing.T) {
	evilCfg := TestingConfig(t)
	evilCfg.Seed = true
	// The attacker controls their own client config.
	evilCfg.AlwaysWantConns = true
	evil, err := NewClient(evilCfg)
	qt.Assert(t, qt.IsNil(err))
	defer evil.Close()
	ih := greetingInfoHash()
	evilT, _ := evil.AddTorrentOpt(AddTorrentOpts{InfoHash: ih, InfoBytes: forgedInfoBytes(t)})
	// With the current bug the public API lets the malicious client load mismatched bytes. If that
	// is ever fixed, simulate a modified client by forcing the info in directly, so this test keeps
	// exercising the leecher side.
	if evilT.Info() == nil {
		b := forgedInfoBytes(t)
		var info metainfo.Info
		qt.Assert(t, qt.IsNil(bencode.Unmarshal(b, &info)))
		evil.lock()
		evilT.metadataBytes = b
		qt.Assert(t, qt.IsNil(evilT.setInfo(&info)))
		evilT.onSetInfo()
		evil.unlock()
	}

	leecher, err := NewClient(TestingConfig(t))
	qt.Assert(t, qt.IsNil(err))
	defer leecher.Close()
	lt, _ := leecher.AddTorrentInfoHash(ih)
	lt.AddClientPeer(evil)
	select {
	case <-lt.GotInfo():
		var got metainfo.Info
		qt.Assert(t, qt.IsNil(bencode.Unmarshal(lt.Metainfo().InfoBytes, &got)))
		t.Errorf("leecher for %v accepted metadata for torrent %q hashing to %v",
			ih, got.Name, metainfo.HashBytes(lt.Metainfo().InfoBytes))
	case <-time.After(3 * time.Second):
	}
}

// Controls: these paths do verify.
func TestPocSetInfoBytesWrongBytesV1AndV2(t *testing.T) {
	cl, err := NewClient(TestingConfig(t))
	qt.Assert(t, qt.IsNil(err))
	defer cl.Close()
	var v2 infohash_v2.T
	v2[0] = 1
	tt, _ := cl.AddTorrentOpt(AddTorrentOpts{InfoHash: greetingInfoHash(), InfoHashV2: g.Some(v2)})
	qt.Check(t, qt.IsNotNil(tt.SetInfoBytes(forgedInfoBytes(t))))
	qt.Check(t, qt.IsNil(tt.Info()))
}

func TestPocSetInfoBytesWrongBytesV2Only(t *testing.T) {
	cl, err := NewClient(TestingConfig(t))
	qt.Assert(t, qt.IsNil(err))
	defer cl.Close()
	var v2 infohash_v2.T
	v2[0] = 1
	tt, _ := cl.AddTorrentOpt(AddTorrentOpts{InfoHashV2: g.Some(v2)})
	qt.Check(t, qt.IsNotNil(tt.SetInfoBytes(forgedInfoBytes(t))))
	qt.Check(t, qt.IsNil(tt.Info()))
}
