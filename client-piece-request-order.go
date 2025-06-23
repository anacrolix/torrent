package torrent

import (
	requestStrategy "github.com/anacrolix/torrent/internal/request-strategy"
	"github.com/anacrolix/torrent/storage"
)

// clientPieceRequestOrderKey is a key for the piece request order map in the Client.
type clientPieceRequestOrderKeyTypes interface {
	storage.TorrentCapacity | *Torrent
}

// clientPieceRequestOrderKey is a generic key type for the piece request order map.
type clientPieceRequestOrderKey[T clientPieceRequestOrderKeyTypes] struct {
	inner T
}

// clientPieceRequestOrderRegularTorrentKey is a concrete key type for regular torrents.
type clientPieceRequestOrderRegularTorrentKey clientPieceRequestOrderKey[*Torrent]

func (c clientPieceRequestOrderRegularTorrentKey) getRequestStrategyInput(cl *Client) requestStrategy.Input {
	return requestStrategyInputSingleTorrent{
		requestStrategyInputCommon: cl.getRequestStrategyInputCommon(),
		t:                          c.inner,
	}
}

// clientPieceRequestOrderSharedStorageTorrentKey is a concrete key type for shared storage torrents.
type clientPieceRequestOrderSharedStorageTorrentKey clientPieceRequestOrderKey[storage.TorrentCapacity]

func (c clientPieceRequestOrderSharedStorageTorrentKey) getRequestStrategyInput(cl *Client) requestStrategy.Input {
	return requestStrategyInputMultiTorrent{
		requestStrategyInputCommon: cl.getRequestStrategyInputCommon(),
		torrents:                   cl.torrentsByShortHash,
		capFunc:                    c.inner,
	}
}

type clientPieceRequestOrderKeySumType interface {
	// getRequestStrategyInput returns the request strategy input for the torrent. It depends on the
	// storage capacity arrangements which is the defining differentiator for the client piece
	// request order keys. This code reads like that stupid second-year software design course I
	// failed 3 times.
	getRequestStrategyInput(cl *Client) requestStrategy.Input
}

type clientPieceRequestOrderValue struct {
	// TODO: Check if we actually ended up needing this?
	torrents map[*Torrent]struct{}
	pieces   *requestStrategy.PieceRequestOrder
}
