package torrent

import (
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	pp "github.com/anacrolix/torrent/peer_protocol"
)

// A peer that sends ut_metadata requests but doesn't read the replies must not be able to grow our
// write buffer without bound. Once it reads again, the conn carries on.
func TestReadLoopWaitsForWriteBuffer(t *testing.T) {
	cl, err := NewClient(TestingConfig(t))
	qt.Assert(t, qt.IsNil(err))
	defer cl.Close()
	// Enough pieces that the info is more than one 16 KiB metadata piece.
	const numPieces = 1000
	info := metainfo.Info{
		Name:        "a",
		PieceLength: 1 << 14,
		Length:      numPieces << 14,
		Pieces:      make([]byte, numPieces*20),
	}
	infoBytes, err := bencode.Marshal(info)
	qt.Assert(t, qt.IsNil(err))
	tt, _ := cl.AddTorrentInfoHash(metainfo.HashBytes(infoBytes))
	cl.lock()
	qt.Assert(t, qt.IsNil(tt.setInfoBytesLocked(infoBytes, nil)))
	ours, theirs := net.Pipe()
	defer theirs.Close()
	c := cl.newConnection(ours, newConnectionOpts{network: "test"})
	c.setTorrent(tt)
	c.LocalLtepProtocolMap = &cl.defaultLocalLtepProtocolMap
	c.PeerExtensionIDs = map[pp.ExtensionName]pp.ExtensionNumber{pp.ExtensionNameMetadata: 1}
	c.startMessageWriter()
	readLoopDone := make(chan struct{})
	go func() {
		defer close(readLoopDone)
		cl.lock()
		defer cl.unlock()
		c.mainReadLoop()
	}()
	cl.unlock()
	defer func() {
		cl.lock()
		c.close()
		cl.unlock()
		ours.Close()
		<-readLoopDone
	}()
	localMetadataId, _, err := c.LocalLtepProtocolMap.LookupId(1)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(localMetadataId, pp.ExtensionNameMetadata))
	request := pp.Message{
		Type:       pp.Extended,
		ExtendedID: 1,
		ExtendedPayload: bencode.MustMarshal(pp.ExtendedMetadataRequestMsg{
			Piece: 0,
			Type:  pp.RequestMetadataExtensionMsgType,
		}),
	}.MustMarshalBinary()
	// Each reply is about 16 KiB, so this would be 4 MiB if it were all buffered.
	const numRequests = 256
	var requestsSent atomic.Int32
	sendDone := make(chan error, 1)
	go func() {
		for range numRequests {
			if _, err := theirs.Write(request); err != nil {
				sendDone <- err
				return
			}
			requestsSent.Add(1)
		}
		sendDone <- nil
	}()
	// net.Pipe is unbuffered, so if our read loop stops reading, the sender stalls.
	var last int32 = -1
	for {
		time.Sleep(50 * time.Millisecond)
		n := requestsSent.Load()
		if n == last {
			break
		}
		last = n
	}
	qt.Check(t, qt.IsTrue(last < numRequests), qt.Commentf("sent %v requests", last))
	c.messageWriter.mu.Lock()
	buffered := c.messageWriter.writeBuffer.Len()
	c.messageWriter.mu.Unlock()
	qt.Check(t, qt.IsTrue(buffered <= maxReadLoopWriteBufferLen+(1<<15)), qt.Commentf("%v bytes buffered", buffered))
	// Reading the replies lets the read loop continue.
	go io.Copy(io.Discard, theirs)
	select {
	case err := <-sendDone:
		qt.Assert(t, qt.IsNil(err))
	case <-time.After(10 * time.Second):
		t.Fatalf("read loop didn't resume, %v requests sent", requestsSent.Load())
	}
}
