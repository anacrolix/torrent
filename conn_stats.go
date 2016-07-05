package torrent

import (
	pp "github.com/anacrolix/torrent/peer_protocol"
)

type ConnStats struct {
	ChunksSent    int64 // Num piece messages sent.
	BytesSent     int64 // Total bytes sent.
	DataBytesSent int64 // Data-only bytes sent.
}

func (cs *ConnStats) wroteMsg(msg pp.Message) {
	switch msg.Type {
	case pp.Piece:
		cs.ChunksSent++
		cs.DataBytesSent += int64(len(msg.Piece))
	}
}

func (cs *ConnStats) wroteBytes(b []byte) {
	cs.BytesSent += int64(len(b))
}
