package httpDataBackend

import (
	"io"
	"net/http"
	"net/url"
	"path"

	"github.com/anacrolix/missinggo/httpfile"
	"github.com/anacrolix/missinggo/httptoo"

	"github.com/anacrolix/torrent/data/pieceStore/dataBackend"
)

var client = http.DefaultClient

type backend struct {
	// Backend URL.
	url url.URL
}

func New(u url.URL) *backend {
	return &backend{
		url: *httptoo.CopyURL(&u),
	}
}

var _ dataBackend.I = &backend{}

func fixErrNotFound(err error) error {
	if err == httpfile.ErrNotFound {
		return dataBackend.ErrNotFound
	}
	return err
}

func (me *backend) urlStr(_path string) string {
	u := me.url
	u.Path = path.Join(u.Path, _path)
	return u.String()
}

func (me *backend) Delete(path string) (err error) {
	err = httpfile.Delete(me.urlStr(path))
	err = fixErrNotFound(err)
	return
}

func (me *backend) GetLength(path string) (ret int64, err error) {
	ret, err = httpfile.GetLength(me.urlStr(path))
	err = fixErrNotFound(err)
	return
}

func (me *backend) Open(path string, flags int) (ret dataBackend.File, err error) {
	ret, err = httpfile.Open(me.urlStr(path), flags)
	err = fixErrNotFound(err)
	return
}

func (me *backend) OpenSection(path string, off, n int64) (ret io.ReadCloser, err error) {
	ret, err = httpfile.OpenSectionReader(me.urlStr(path), off, n)
	err = fixErrNotFound(err)
	return
}
