package torrent

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"

	"github.com/RoaringBitmap/roaring/v2"
	"github.com/james-lawrence/torrent/dht/int160"
	"github.com/james-lawrence/torrent/internal/errorsx"
)

type BitmapStore interface {
	Delete(id int160.T) error
	Read(id int160.T) (*roaring.Bitmap, error)
	Write(id int160.T, bm *roaring.Bitmap) error
}

func NewBitmapCache(root string) bitmapfilestore {
	if err := os.MkdirAll(root, 0700); err != nil {
		log.Println("unable to ensure bitmap cache root directory", err)
	}

	return bitmapfilestore{
		root: root,
	}
}

type bitmapfilestore struct {
	root string
}

// Delete implements BitmapStore.
func (t bitmapfilestore) Delete(id int160.T) error {
	return os.Remove(t.path(id))
}

func (t bitmapfilestore) path(id int160.T) string {
	return filepath.Join(t.root, fmt.Sprintf("%s.bitmap", id.String()))
}

func (t bitmapfilestore) Read(id int160.T) (*roaring.Bitmap, error) {
	p := t.path(id)
	src, err := os.Open(p)
	if errors.Is(err, fs.ErrNotExist) {
		return roaring.New(), nil
	} else if err != nil {
		return nil, errorsx.Wrapf(err, "unable to read bitmap from %s", p)
	}
	defer src.Close()

	bm := roaring.New()

	if _, err := bm.ReadFrom(src); err != nil {
		return nil, errorsx.Wrapf(err, "unable to read bitmap from %s", p)
	}

	return bm, nil
}

func (t bitmapfilestore) Write(id int160.T, bitmap *roaring.Bitmap) error {
	p := t.path(id)
	dst, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_SYNC, 0600)
	if err != nil {
		return errorsx.Wrapf(err, "unable to write bitmap to %s", p)
	}
	defer dst.Close()
	if _, err := bitmap.WriteTo(dst); err != nil {
		return errorsx.Wrapf(err, "unable to write bitmap to %s", p)
	}
	return nil
}
