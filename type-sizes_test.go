package torrent

import (
	"reflect"
	"testing"

	"github.com/anacrolix/chansync"
	g "github.com/anacrolix/generics"
	"github.com/go-quicktest/qt"
)

func testSizeof[T any](t *testing.T, max g.Option[uintptr]) {
	ty := reflect.TypeFor[T]()
	size := ty.Size()
	t.Logf("%v has size %v", ty, size)
	if max.Ok {
		qt.Check(t, qt.IsTrue(size <= max.Value), qt.Commentf("size of %v is %v, expected <= %v", ty, size, max.Value))
	}
}

func checkSizeLessThan[T any](t *testing.T, max uintptr) {
	testSizeof[T](t, g.Some(max))
}

func justLogSizeof[T any](t *testing.T) {
	testSizeof[T](t, g.None[uintptr]())
}

func TestTypeSizes(t *testing.T) {
	justLogSizeof[[]*File](t)
	checkSizeLessThan[Piece](t, 296)
	justLogSizeof[map[*Peer]struct{}](t)
	justLogSizeof[chansync.BroadcastCond](t)

	justLogSizeof[g.Option[[32]byte]](t)
	justLogSizeof[[]byte](t)
}
