package alloclim

import (
	"context"
	"testing"
	"time"

	_ "github.com/anacrolix/envpprof"
	qt "github.com/frankban/quicktest"
)

func TestReserveOverMax(t *testing.T) {
	c := qt.New(t)
	l := &Limiter{Max: 10}
	r := l.Reserve(20)
	c.Assert(r.Wait(context.Background()), qt.IsNotNil)
}

func TestImmediateAllow(t *testing.T) {
	c := qt.New(t)
	l := &Limiter{Max: 10}
	r := l.Reserve(10)
	c.Assert(r.Wait(context.Background()), qt.IsNil)
}

func TestSimpleSequence(t *testing.T) {
	c := qt.New(t)
	l := &Limiter{Max: 10}
	rs := make([]*Reservation, 0)
	rs = append(rs, l.Reserve(6))
	rs = append(rs, l.Reserve(5))
	rs = append(rs, l.Reserve(5))
	c.Assert(rs[0].Wait(context.Background()), qt.IsNil)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(time.Nanosecond))
	c.Assert(rs[1].Wait(ctx), qt.Equals, context.DeadlineExceeded)
	go cancel()
	ctx, cancel = context.WithCancel(context.Background())
	go cancel()
	c.Assert(rs[2].Wait(ctx), qt.Equals, context.Canceled)
	go rs[0].Release()
	ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(time.Second))
	c.Assert(rs[1].Wait(ctx), qt.IsNil)
	go rs[1].Release()
	c.Assert(rs[2].Wait(ctx), qt.IsNil)
	go rs[2].Release()
	go cancel()
	rs[2].Release()
	rs[1].Release()
	c.Assert(l.Value(), qt.Equals, l.Max)
}

func TestSequenceWithCancel(t *testing.T) {
	c := qt.New(t)
	l := &Limiter{Max: 10}
	rs := make([]*Reservation, 0)
	rs = append(rs, l.Reserve(6))
	rs = append(rs, l.Reserve(6))
	rs = append(rs, l.Reserve(4))
	rs = append(rs, l.Reserve(4))
	c.Assert(rs[0].Cancel(), qt.IsFalse)
	c.Assert(func() { rs[1].Release() }, qt.PanicMatches, "not resolved")
	c.Assert(rs[1].Cancel(), qt.IsTrue)
	c.Assert(rs[2].Wait(context.Background()), qt.IsNil)
	rs[0].Release()
	c.Assert(rs[3].Wait(context.Background()), qt.IsNil)
	c.Assert(l.Value(), qt.Equals, int64(2))
	rs[1].Release()
	rs[2].Release()
	rs[3].Release()
	c.Assert(l.Value(), qt.Equals, l.Max)
}

func TestCancelWhileWaiting(t *testing.T) {
	c := qt.New(t)
	l := &Limiter{Max: 10}
	rs := make([]*Reservation, 0)
	rs = append(rs, l.Reserve(6))
	rs = append(rs, l.Reserve(6))
	rs = append(rs, l.Reserve(4))
	rs = append(rs, l.Reserve(4))
	go rs[1].Cancel()
	err := rs[1].Wait(context.Background())
	c.Assert(err, qt.IsNotNil)
	err = rs[2].Wait(context.Background())
	c.Assert(err, qt.IsNil)
	ctx, cancel := context.WithCancel(context.Background())
	go cancel()
	err = rs[3].Wait(ctx)
	c.Assert(err, qt.Equals, context.Canceled)
	rs[0].Drop()
	err = rs[3].Wait(ctx)
	c.Assert(err, qt.IsNil)
}
