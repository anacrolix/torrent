// PacketConn Multi-consumer
package netpcmc

import (
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/anacrolix/log"
)

type Multi struct {
	Conn             net.PacketConn
	MinReadBytes     int
	MaxBufferedReads int

	mu     sync.Mutex
	buffer []read
	offset int
}

type read struct {
	payload []byte
	from    net.Addr
}

func (me *Multi) NewConsumer(logger log.Logger) net.PacketConn {
	return &consumer{
		m:          me,
		PacketConn: me.Conn,
		next:       me.offset,
		logger:     logger,
	}
}

func (me *Multi) readNext(n int) error {
	b := make([]byte, n)
	n, addr, err := me.Conn.ReadFrom(b)
	if err != nil {
		return err
	}
	me.buffer = append(me.buffer, read{
		payload: b[:n],
		from:    addr,
	})
	if len(me.buffer) > me.MaxBufferedReads {
		me.offset++
		me.buffer = me.buffer[1:]
	}
	return nil
}

func (me *Multi) bufferOffset(offset int, n int) error {
	offset -= me.offset
	if offset == len(me.buffer) {
		return me.readNext(n)
	}
	return nil
}

type consumer struct {
	m *Multi
	net.PacketConn
	logger log.Logger

	next int
}

func (me *consumer) ReadFrom(b []byte) (n int, addr net.Addr, err error) {
	me.m.mu.Lock()
	defer me.m.mu.Unlock()
	if me.next < me.m.offset {
		me.logger.Levelf(log.Warning, "next packet already discarded")
		me.next = me.m.offset
	}
	err = me.m.bufferOffset(me.next, len(b))
	if err != nil {
		err = fmt.Errorf("buffering next packet: %w", err)
		return
	}
	read := me.m.buffer[me.next-me.m.offset]
	n = copy(b, read.payload)
	addr = read.from
	me.next++
	return
}

func (me *consumer) Close() error {
	return nil
}

func (me *consumer) SetDeadline(t time.Time) error {
	panic("unimplemented")
}
func (me *consumer) SetReadDeadline(t time.Time) error {
	panic("unimplemented")
}
func (me *consumer) SetWriteDeadline(t time.Time) error {
	panic("unimplemented")
}
