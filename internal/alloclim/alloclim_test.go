package alloclim

import (
	"context"
	"testing"
	"time"

	_ "github.com/anacrolix/envpprof"
	qt "github.com/go-quicktest/qt"
)

func TestReserveOverMax(t *testing.T) {
	l := &Limiter{Max: 10}
	r := l.Reserve(20)
	qt.Assert(t, qt.IsNotNil(r.Wait(context.Background())))
}

func TestImmediateAllow(t *testing.T) {
	l := &Limiter{Max: 10}
	r := l.Reserve(10)
	qt.Assert(t, qt.IsNil(r.Wait(context.Background())))
}

func TestSimpleSequence(t *testing.T) {
	l := &Limiter{Max: 10}
	rs := make([]*Reservation, 0)
	rs = append(rs, l.Reserve(6))
	rs = append(rs, l.Reserve(5))
	rs = append(rs, l.Reserve(5))
	qt.Assert(t, qt.IsNil(rs[0].Wait(context.Background())))
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(time.Nanosecond))
	qt.Assert(t, qt.Equals(rs[1].Wait(ctx), context.DeadlineExceeded))
	go cancel()
	ctx, cancel = context.WithCancel(context.Background())
	go cancel()
	qt.Assert(t, qt.Equals(rs[2].Wait(ctx), context.Canceled))
	go rs[0].Release()
	ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(time.Second))
	qt.Assert(t, qt.IsNil(rs[1].Wait(ctx)))
	go rs[1].Release()
	qt.Assert(t, qt.IsNil(rs[2].Wait(ctx)))
	go rs[2].Release()
	go cancel()
	rs[2].Release()
	rs[1].Release()
	qt.Assert(t, qt.Equals(l.Value(), l.Max))
}

func TestSequenceWithCancel(t *testing.T) {
	l := &Limiter{Max: 10}
	rs := make([]*Reservation, 0)
	rs = append(rs, l.Reserve(6))
	rs = append(rs, l.Reserve(6))
	rs = append(rs, l.Reserve(4))
	rs = append(rs, l.Reserve(4))
	qt.Assert(t, qt.IsFalse(rs[0].Cancel()))
	qt.Assert(t, qt.PanicMatches(func() { rs[1].Release() }, "not resolved"))
	qt.Assert(t, qt.IsTrue(rs[1].Cancel()))
	qt.Assert(t, qt.IsNil(rs[2].Wait(context.Background())))
	rs[0].Release()
	qt.Assert(t, qt.IsNil(rs[3].Wait(context.Background())))
	qt.Assert(t, qt.Equals(l.Value(), int64(2)))
	rs[1].Release()
	rs[2].Release()
	rs[3].Release()
	qt.Assert(t, qt.Equals(l.Value(), l.Max))
}

func TestCancelWhileWaiting(t *testing.T) {
	l := &Limiter{Max: 10}
	rs := make([]*Reservation, 0)
	rs = append(rs, l.Reserve(6))
	rs = append(rs, l.Reserve(6))
	rs = append(rs, l.Reserve(4))
	rs = append(rs, l.Reserve(4))
	go rs[1].Cancel()
	err := rs[1].Wait(context.Background())
	qt.Assert(t, qt.IsNotNil(err))
	err = rs[2].Wait(context.Background())
	qt.Assert(t, qt.IsNil(err))
	ctx, cancel := context.WithCancel(context.Background())
	go cancel()
	err = rs[3].Wait(ctx)
	qt.Assert(t, qt.Equals(err, context.Canceled))
	rs[0].Drop()
	err = rs[3].Wait(ctx)
	qt.Assert(t, qt.IsNil(err))
}
