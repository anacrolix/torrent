package cstate

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

type Shared struct {
	done context.CancelCauseFunc
}

type T interface {
	Update(context.Context, *Shared) T
}

func Idle(next T, t time.Duration, cond *sync.Cond, signals ...*sync.Cond) idle {
	return idle{
		timeout: t,
		next:    next,
		cond:    cond,
		signals: signals,
	}
}

type idle struct {
	timeout time.Duration
	next    T
	cond    *sync.Cond
	signals []*sync.Cond
}

func (t idle) monitor(ctx context.Context, target *sync.Cond, signals ...*sync.Cond) {
	ctx, done := context.WithCancel(ctx)
	defer func() {
		for _, s := range signals {
			s.Broadcast()
		}
	}()
	defer done()

	for _, s := range signals {
		go func() {
			for {
				s.L.Lock()
				s.Wait()
				s.L.Unlock()
				target.Broadcast()
				select {
				case <-ctx.Done():
					return
				default:
				}
			}
		}()
	}

	if t.timeout > 0 {
		go func() {
			select {
			case <-time.After(t.timeout):
				t.cond.Broadcast()
			case <-ctx.Done():
			}
		}()
	}

	target.L.Lock()
	target.Wait()
	target.L.Unlock()
}

func (t idle) Update(ctx context.Context, c *Shared) T {
	t.monitor(ctx, t.cond, t.signals...)

	log.Printf("%s - awake\n", t)

	return t.next
}

func (t idle) String() string {
	return fmt.Sprintf("%T - %s - idle", t.next, t.next)
}

func Failure(cause error) failed {
	return failed{cause: cause}
}

type failed struct {
	cause error
}

func (t failed) Update(ctx context.Context, c *Shared) T {
	c.done(t.cause)
	return nil
}

func Fn(fn fn) fn {
	return fn
}

type fn func(context.Context, *Shared) T

func (t fn) Update(ctx context.Context, s *Shared) T {
	return t(ctx, s)
}

func Run(ctx context.Context, s T) error {
	ctx, cancelled := context.WithCancelCause(ctx)
	var (
		m = Shared{
			done: cancelled,
		}
	)

	for {
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		default:
			// log.Printf("running %T %s\n", s, s)
			s = s.Update(ctx, &m)
		}

		if s == nil {
			return context.Cause(ctx)
		}
	}
}
