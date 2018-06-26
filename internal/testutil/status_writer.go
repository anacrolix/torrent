package testutil

import (
	"fmt"
	"io"
	"net/http"
	"sync"

	"github.com/anacrolix/missinggo"
)

type StatusWriter interface {
	WriteStatus(io.Writer)
}

// The key is the route pattern. The value is nil when the resource is
// released.
var (
	mu  sync.Mutex
	sws = map[string]StatusWriter{}
)

func ExportStatusWriter(sw StatusWriter, path string) (release func()) {
	pattern := fmt.Sprintf("/%s/%s", missinggo.GetTestName(), path)
	release = func() {
		mu.Lock()
		defer mu.Unlock()
		sws[pattern] = nil
	}
	mu.Lock()
	defer mu.Unlock()
	if curSw, ok := sws[pattern]; ok {
		if curSw != nil {
			panic(fmt.Sprintf("%q still in use", pattern))
		}
		sws[pattern] = sw
		return
	}
	http.HandleFunc(
		pattern,
		func(w http.ResponseWriter, r *http.Request) {
			sw := sws[pattern]
			if sw == nil {
				http.NotFound(w, r)
				return
			}
			sw.WriteStatus(w)
		},
	)
	sws[pattern] = sw
	return
}
