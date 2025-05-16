package torrent

import (
	"github.com/james-lawrence/torrent/internal/errorsx"
	"github.com/pkg/errors"
)

func ErrTorrentClosed() error {
	return errors.New("torrent closed")
}

const (
	ErrTorrentNotActive = errorsx.String("torrent not active")
)
