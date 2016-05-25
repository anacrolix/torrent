package ratelimit

import (
	"github.com/stretchr/testify/assert"
	"testing"
	"time"
)

func TestRateLimit_ApplyForSendByte(t *testing.T) {
	r := new(RateLimit)
	r.RestByte = 10
	r.MaxByte = 10
	done1 := false
	done2 := false
	done3 := false
	go func() {
		r.ApplyForSendByte(11)
		done1 = true
	}()
	go func() {
		r.ApplyForSendByte(9)
		done2 = true
	}()
	go func() {
		r.ApplyForSendByte(9)
		done3 = true
	}()

	time.Sleep(1 * time.Second)
	assert.Equal(t, done1, false)
	assert.Equal(t, done2 || done3, true)
	assert.Equal(t, done2 && done3, false)
}

func TestRateLimit_PeriodicallyIncRate(t *testing.T) {

	r := new(RateLimit)
	r.RestByte = 10
	r.MaxByte = 10 * 1024 * 1024
	r.PeriodicallyIncRate()
	time.Sleep(1 * time.Second)
	assert.Equal(t, r.RestByte > 10, true)
}
