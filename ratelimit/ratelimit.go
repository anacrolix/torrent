package ratelimit

import (
	"sync"
	"time"
)

type RateLimit struct {
	MaxByte  uint32 // how many bytes can be send every second
	RestByte uint32 // how many rest bytes can be seed
	lock     sync.Mutex
	period   time.Duration
}

func (r *RateLimit) PeriodicallyIncRate() {

	if r.period == 0 {
		r.period = time.Millisecond * 10
	}

	//assure period less than 1s
	if r.period > time.Second {
		r.period = time.Second
	}

	if r.RestByte == 0 {
		r.RestByte = r.MaxByte
	}
	bytes := r.MaxByte / uint32((time.Second / r.period))
	if bytes == 0{
		panic("too small maxbyte")
	}
	ticker := time.NewTicker(r.period)
	go func() {
		for range ticker.C {
			if r.RestByte >= r.MaxByte{
				continue
			}
			r.lock.Lock()
			r.RestByte += bytes
			if r.RestByte > r.MaxByte{
				r.RestByte = r.MaxByte
			}
			r.lock.Unlock()
		}
	}()
}

func (r *RateLimit) ApplyForSendByte(n uint32) {
	for {
		r.lock.Lock()
		if r.RestByte >= n {
			r.RestByte -= n
			r.lock.Unlock()
			break
		} else {
			r.lock.Unlock()
			// just sleep
			time.Sleep(r.period)
		}

	}
}
