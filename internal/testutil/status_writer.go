package testutil

import (
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"testing"

	"github.com/anacrolix/missinggo"
	"github.com/stretchr/testify/require"
)

type StatusWriter interface {
	WriteStatus(io.Writer)
}

// Use StatusServer instead to allow -count > 1 when testing.
func ExportStatusWriter(sw StatusWriter, path string) {
	http.HandleFunc(
		fmt.Sprintf("/%s/%s", missinggo.GetTestName(), path),
		func(w http.ResponseWriter, r *http.Request) {
			sw.WriteStatus(w)
		},
	)
}

type StatusServer struct {
	sm http.ServeMux
	l  net.Listener
}

func NewStatusServer(t *testing.T) (ret *StatusServer) {
	l, err := net.Listen("tcp", "localhost:0")
	require.NoError(t, err)
	ret = &StatusServer{
		l: l,
	}
	log.Printf("serving status at %q", l.Addr())
	go http.Serve(l, &ret.sm)
	return
}

func (me *StatusServer) HandleStatusWriter(sw StatusWriter, path string) {
	me.sm.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		sw.WriteStatus(w)
	})
}

func (me StatusServer) Close() {
	me.l.Close()
}
