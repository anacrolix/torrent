package torrent

import (
	"github.com/anacrolix/torrent/storage"
)

type clientPieceRequestOrderKeyTypes interface {
	storage.TorrentCapacity | *Torrent
}

type clientPieceRequestOrderKey[T clientPieceRequestOrderKeyTypes] struct {
	inner T
}

func (me clientPieceRequestOrderKey[T]) isAClientPieceRequestOrderKeyType() {}

type clientPieceRequestOrderKeySumType interface {
	isAClientPieceRequestOrderKeyType()
}
