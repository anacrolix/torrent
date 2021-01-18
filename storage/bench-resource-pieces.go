package storage

import (
	"bytes"
	"io"
	"io/ioutil"
	"sync"
	"testing"

	qt "github.com/frankban/quicktest"
)

func BenchmarkPieceMarkComplete(tb testing.TB, pi PieceImpl, data []byte) {
	c := qt.New(tb)
	var wg sync.WaitGroup
	for off := int64(0); off < int64(len(data)); off += chunkSize {
		wg.Add(1)
		go func(off int64) {
			defer wg.Done()
			n, err := pi.WriteAt(data[off:off+chunkSize], off)
			if err != nil {
				panic(err)
			}
			if n != chunkSize {
				panic(n)
			}
		}(off)
	}
	wg.Wait()
	// This might not apply if users of this benchmark don't cache with the expected capacity.
	c.Assert(pi.Completion(), qt.Equals, Completion{Complete: false, Ok: true})
	c.Assert(pi.MarkComplete(), qt.IsNil)
	c.Assert(pi.Completion(), qt.Equals, Completion{true, true})
	readData, err := ioutil.ReadAll(io.NewSectionReader(pi, 0, int64(len(data))))
	c.Assert(err, qt.IsNil)
	c.Assert(len(readData), qt.Equals, len(data))
	c.Assert(bytes.Equal(readData, data), qt.IsTrue)
}
