package storage

import (
	"cmp"
	"expvar"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"slices"
	"sync"
	"sync/atomic"
)

var (
	sharedFiles sharedFilesInterface = regularFsSharedFiles{}

	wipSharedFilesPool = sharedFilesType{m: make(map[string]*sharedFile)}
)

type regularFsSharedFiles struct{}

func (r regularFsSharedFiles) Open(name string) (sharedFileIf, error) {
	return os.Open(name)
}

type sharedFileIf interface {
	io.ReaderAt
	io.Closer
}

type sharedFilesInterface interface {
	Open(name string) (sharedFileIf, error)
}

func init() {
	http.HandleFunc("/debug/shared-files", func(w http.ResponseWriter, r *http.Request) {
		wipSharedFilesPool.WriteDebug(w)
	})
}

type sharedFilesType struct {
	mu sync.Mutex
	m  map[string]*sharedFile
}

func (sharedFiles *sharedFilesType) WriteDebug(w io.Writer) {
	sharedFiles.mu.Lock()
	defer sharedFiles.mu.Unlock()
	byRefs := slices.SortedFunc(maps.Keys(sharedFiles.m), func(a, b string) int {
		return cmp.Or(
			sharedFiles.m[b].refs-sharedFiles.m[a].refs,
			cmp.Compare(a, b))
	})
	for _, key := range byRefs {
		sf := sharedFiles.m[key]
		fmt.Fprintf(w, "%v: refs=%v, name=%v\n", key, sf.refs, sf.f.Name())
	}
}

// How many opens wouldn't have been needed with singleflight.
var sharedFilesWastedOpens = expvar.NewInt("sharedFilesWastedOpens")

func (me *sharedFilesType) Open(name string) (ret *sharedFileRef, err error) {
	me.mu.Lock()
	sf, ok := me.m[name]
	if !ok {
		me.mu.Unlock()
		// Can singleflight here...
		var f *os.File
		f, err = os.Open(name)
		if err != nil {
			return
		}
		me.mu.Lock()
		sf, ok = me.m[name]
		if ok {
			sharedFilesWastedOpens.Add(1)
			f.Close()
		} else {
			sf = &sharedFile{pool: me, f: f}
			me.m[name] = sf
		}
	}
	ret = sf.newRef()
	me.mu.Unlock()
	return
}

type sharedFile struct {
	pool *sharedFilesType
	f    *os.File
	// Could do this with weakrefs... Wonder if it works well with OS resources like that.
	refs int
}

func (me *sharedFile) newRef() *sharedFileRef {
	me.refs++
	return &sharedFileRef{
		sf:      me,
		inherit: me.f,
	}
}

type inherit interface {
	io.ReaderAt
}

type sharedFileRef struct {
	// Only methods that are safe for concurrent use.
	inherit
	sf     *sharedFile
	closed atomic.Bool
}

func (me *sharedFileRef) Close() (err error) {
	if !me.closed.CompareAndSwap(false, true) {
		return
	}
	me.inherit = nil
	me.sf.pool.mu.Lock()
	me.sf.refs--
	me.sf.pool.mu.Unlock()
	me.sf = nil
	return
}
