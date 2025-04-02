package torrent

import "github.com/pkg/errors"

func ErrTorrentClosed() error {
	return errors.New("torrent closed")
}
