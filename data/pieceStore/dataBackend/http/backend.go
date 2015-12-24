package httpDataBackend

import (
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"sync"
	"time"

	"github.com/anacrolix/missinggo/httpfile"
	"github.com/anacrolix/missinggo/httptoo"

	"github.com/anacrolix/torrent/data/pieceStore/dataBackend"
)

// A net.Conn that releases a handle from the dial pool when closed.
type dialPoolNetConn struct {
	net.Conn

	dialPool chan struct{}
	mu       sync.Mutex
	released bool
}

func (me *dialPoolNetConn) Close() error {
	err := me.Conn.Close()
	me.mu.Lock()
	if !me.released {
		<-me.dialPool
		me.released = true
	}
	me.mu.Unlock()
	return err
}

type backend struct {
	// Backend URL.
	url url.URL

	FS httpfile.FS
}

func New(u url.URL) *backend {
	// Limit concurrent connections at a time.
	dialPool := make(chan struct{}, 5)
	// Allows an extra connection through once a second to break deadlocks and
	// help with stalls.
	ticker := time.NewTicker(time.Second)
	return &backend{
		url: *httptoo.CopyURL(&u),
		FS: httpfile.FS{
			Client: &http.Client{
				Transport: &http.Transport{
					Dial: func(_net, addr string) (net.Conn, error) {
						select {
						case dialPool <- struct{}{}:
						case <-ticker.C:
							go func() { dialPool <- struct{}{} }()
						}
						nc, err := net.Dial(_net, addr)
						if err != nil {
							<-dialPool
							return nil, err
						}
						return &dialPoolNetConn{
							Conn:     nc,
							dialPool: dialPool,
						}, nil
					},
				},
			},
			// Client: http.DefaultClient,
		},
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
	err = me.FS.Delete(me.urlStr(path))
	err = fixErrNotFound(err)
	return
}

func (me *backend) GetLength(path string) (ret int64, err error) {
	ret, err = me.FS.GetLength(me.urlStr(path))
	err = fixErrNotFound(err)
	return
}

func (me *backend) Open(path string, flags int) (ret dataBackend.File, err error) {
	ret, err = me.FS.Open(me.urlStr(path), flags)
	err = fixErrNotFound(err)
	return
}

func (me *backend) OpenSection(path string, off, n int64) (ret io.ReadCloser, err error) {
	ret, err = me.FS.OpenSectionReader(me.urlStr(path), off, n)
	err = fixErrNotFound(err)
	return
}
