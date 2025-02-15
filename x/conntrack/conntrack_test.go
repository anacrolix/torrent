package conntrack

import (
	"context"
	"math"
	"math/rand"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/anacrolix/stm"
	"github.com/bradfitz/iter"
	"github.com/james-lawrence/torrent/internal/stmutil"
	"github.com/stretchr/testify/assert"
)

func entry(id int) Entry {
	return Entry{"", "", strconv.FormatInt(int64(id), 10)}
}

func TestWaitingForSameEntry(t *testing.T) {
	i := NewInstance()
	i.SetMaxEntries(1)
	i.Timeout = func(Entry) time.Duration {
		return 0
	}
	e1h1 := i.WaitDefault(context.Background(), entry(1))
	gotE2s := make(chan struct{})
	for range iter.N(2) {
		go func() {
			i.WaitDefault(context.Background(), entry(2))
			gotE2s <- struct{}{}
		}()
	}
	gotE1 := make(chan struct{})
	var e1h2 *EntryHandle
	go func() {
		e1h2 = i.WaitDefault(context.Background(), entry(1))
		gotE1 <- struct{}{}
	}()
	select {
	case <-gotE1:
	case <-gotE2s:
		t.FailNow()
	}
	go e1h1.Done()
	go e1h2.Done()
	<-gotE2s
	<-gotE2s
}

func TestInstanceSetNoMaxEntries(t *testing.T) {
	i := NewInstance()
	i.SetMaxEntries(0)
	var wg sync.WaitGroup
	wait := func(e Entry, p priority) {
		i.Wait(context.Background(), e, "", p)
		wg.Done()
	}
	for _, e := range []Entry{entry(0), entry(1)} {
		for _, p := range []priority{math.MinInt32, math.MaxInt32} {
			wg.Add(1)
			go wait(e, p)
		}
	}
	waitForNumWaiters := func(num int) {
		stm.Atomically(stm.VoidOperation(func(tx *stm.Tx) {
			tx.Assert(i.waiters.Get(tx).Len() == num)
		}))
	}
	waitForNumWaiters(4)
	i.SetNoMaxEntries()
	waitForNumWaiters(0)
	wg.Wait()
}

func TestWaitReturnsNilContextCompleted(t *testing.T) {
	i := NewInstance()
	i.SetMaxEntries(0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.Nil(t, i.WaitDefault(ctx, entry(0)))
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Millisecond)
	assert.Nil(t, i.WaitDefault(ctx, entry(1)))
	cancel()
}

func TestWaitContextCanceledButRoomForEntry(t *testing.T) {
	i := NewInstance()
	i.SetMaxEntries(1)
	ctx, cancel := context.WithCancel(context.Background())
	go cancel()
	eh := i.WaitDefault(ctx, entry(0))
	if eh == nil {
		assert.Error(t, ctx.Err())
	} else {
		eh.Done()
	}
}

func TestUnlimitedInstance(t *testing.T) {
	i := NewInstance()
	i.SetNoMaxEntries()
	i.Timeout = func(Entry) time.Duration { return 0 }
	eh := i.WaitDefault(context.Background(), entry(0))
	assert.NotNil(t, eh)
	assert.EqualValues(t, stmutil.GetLeft(stm.AtomicGet(i.entries).Get(eh.e)).(stmutil.Settish[*EntryHandle]).Len(), 1)
	eh.Done()
	assert.Nil(t, stmutil.GetLeft(stm.AtomicGet(i.entries).Get(eh.e)))
}

func TestUnlimitedInstanceContextCanceled(t *testing.T) {
	i := NewInstance()
	i.SetNoMaxEntries()
	i.Timeout = func(Entry) time.Duration { return 0 }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	eh := i.WaitDefault(ctx, entry(0))
	assert.NotNil(t, eh)
	assert.EqualValues(t, stmutil.GetLeft(stm.AtomicGet(i.entries).Get(eh.e)).(stmutil.Settish[*EntryHandle]).Len(), 1)
	eh.Done()
	assert.Nil(t, stmutil.GetLeft(stm.AtomicGet(i.entries).Get(eh.e)))
}

func TestContextCancelledWhileWaiting(t *testing.T) {
	i := NewInstance()
	i.SetMaxEntries(0)
	ctx, cancel := context.WithCancel(context.Background())
	assert.EqualValues(t, stm.AtomicGet(i.waiters).Len(), 0)
	waitReturned := make(chan struct{})
	go func() {
		eh := i.WaitDefault(ctx, entry(0))
		assert.Nil(t, eh)
		close(waitReturned)
	}()
	stm.Atomically(stm.VoidOperation(func(tx *stm.Tx) {
		tx.Assert(i.waiters.Get(tx).Len() == 1)
	}))
	cancel()
	<-waitReturned
	assert.EqualValues(t, stm.AtomicGet(i.entries).Len(), 0)
	assert.EqualValues(t, stm.AtomicGet(i.waiters).Len(), 0)
}

func TestRaceWakeAndContextCompletion(t *testing.T) {
	i := NewInstance()
	i.SetMaxEntries(1)
	eh0 := i.WaitDefault(context.Background(), entry(0))
	ctx, cancel := context.WithCancel(context.Background())
	waitReturned := make(chan struct{})
	go func() {
		eh1 := i.WaitDefault(ctx, entry(1))
		if eh1 != nil {
			eh1.Forget()
		}
		close(waitReturned)
	}()
	go cancel()
	go eh0.Forget()
	<-waitReturned
	cancel()
	eh0.Forget()
	assert.EqualValues(t, stm.AtomicGet(i.entries).(stmutil.Lenner).Len(), 0)
	assert.EqualValues(t, stm.AtomicGet(i.waiters).(stmutil.Lenner).Len(), 0)
}

func TestPriority(t *testing.T) {
	testPriority(t, 100)
}

func testPriority(t testing.TB, n int) {
	i := NewInstance()
	i.SetMaxEntries(0)
	ehs := make(chan *EntryHandle)
	for _, j := range rand.Perm(n) {
		go func(j int) {
			ehs <- i.Wait(context.Background(), entry(j), "", priority(j))
		}(j)
	}
	stm.Atomically(stm.VoidOperation(func(tx *stm.Tx) {
		tx.Assert(i.waiters.Get(tx).Len() == n)
	}))
	select {
	case <-ehs:
		panic("non should have passed")
	default:
	}
	i.SetMaxEntries(1)
	for j := range iter.N(n) {
		eh := <-ehs
		assert.EqualValues(t, entry(n-j-1), eh.e)
		//log.Print(eh.priority)
		eh.Forget()
	}
}

func benchmarkPriority(b *testing.B, n int) {
	for range iter.N(b.N) {
		testPriority(b, n)
	}
}

func BenchmarkPriority1(b *testing.B) {
	benchmarkPriority(b, 1)
}

func BenchmarkPriority10(b *testing.B) {
	benchmarkPriority(b, 10)
}

func BenchmarkPriority100(b *testing.B) {
	benchmarkPriority(b, 100)
}
