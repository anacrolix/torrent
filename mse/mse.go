// https://wiki.vuze.com/w/Message_Stream_Encryption

package mse

import (
	"bytes"
	"crypto/rand"
	"crypto/rc4"
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"math/big"
	"sync"

	"github.com/bradfitz/iter"
)

const (
	maxPadLen = 512

	cryptoMethodPlaintext = 1
	cryptoMethodRC4       = 2
)

var (
	// Prime P according to the spec, and G, the generator.
	p, g big.Int
	// The rand.Int max arg for use in newPadLen()
	newPadLenMax big.Int
	// For use in initer's hashes
	req1 = []byte("req1")
	req2 = []byte("req2")
	req3 = []byte("req3")
)

func init() {
	p.SetString("0xFFFFFFFFFFFFFFFFC90FDAA22168C234C4C6628B80DC1CD129024E088A67CC74020BBEA63B139B22514A08798E3404DDEF9519B3CD3A431B302B0A6DF25F14374FE1356D6D51C245E485B576625E7EC6F44C42E9A63A36210000000000090563", 0)
	g.SetInt64(2)
	newPadLenMax.SetInt64(maxPadLen + 1)
}

func hash(parts ...[]byte) []byte {
	h := sha1.New()
	for _, p := range parts {
		n, err := h.Write(p)
		if err != nil {
			panic(err)
		}
		if n != len(p) {
			panic(n)
		}
	}
	return h.Sum(nil)
}

func newEncrypt(initer bool, s []byte, skey []byte) (c *rc4.Cipher) {
	c, err := rc4.NewCipher(hash([]byte(func() string {
		if initer {
			return "keyA"
		} else {
			return "keyB"
		}
	}()), s, skey))
	if err != nil {
		panic(err)
	}
	var burnSrc, burnDst [1024]byte
	c.XORKeyStream(burnDst[:], burnSrc[:])
	return
}

type cipherReader struct {
	c *rc4.Cipher
	r io.Reader
}

func (me *cipherReader) Read(b []byte) (n int, err error) {
	be := make([]byte, len(b))
	n, err = me.r.Read(be)
	me.c.XORKeyStream(b[:n], be[:n])
	return
}

func newCipherReader(c *rc4.Cipher, r io.Reader) io.Reader {
	return &cipherReader{c, r}
}

type cipherWriter struct {
	c *rc4.Cipher
	w io.Writer
}

func (me *cipherWriter) Write(b []byte) (n int, err error) {
	be := make([]byte, len(b))
	me.c.XORKeyStream(be, b)
	n, err = me.w.Write(be)
	if n != len(be) {
		// The cipher will have advanced beyond the callers stream position.
		// We can't use the cipher anymore.
		me.c = nil
	}
	return
}

func newCipherWriter(c *rc4.Cipher, w io.Writer) io.Writer {
	return &cipherWriter{c, w}
}

func readY(r io.Reader) (y big.Int, err error) {
	var b [96]byte
	_, err = io.ReadFull(r, b[:])
	if err != nil {
		return
	}
	y.SetBytes(b[:])
	return
}

func newX() big.Int {
	var X big.Int
	X.SetBytes(func() []byte {
		var b [20]byte
		_, err := rand.Read(b[:])
		if err != nil {
			panic(err)
		}
		return b[:]
	}())
	return X
}

func (h *handshake) postY(x *big.Int) error {
	var y big.Int
	y.Exp(&g, x, &p)
	b := y.Bytes()
	if len(b) != 96 {
		panic(len(b))
	}
	return h.postWrite(b)
}

func (h *handshake) establishS() (err error) {
	x := newX()
	h.postY(&x)
	var b [96]byte
	_, err = io.ReadFull(h.conn, b[:])
	if err != nil {
		return
	}
	var Y big.Int
	Y.SetBytes(b[:])
	h.s.Exp(&Y, &x, &p)
	return
}

func newPadLen() int64 {
	i, err := rand.Int(rand.Reader, &newPadLenMax)
	if err != nil {
		panic(err)
	}
	ret := i.Int64()
	if ret < 0 || ret > maxPadLen {
		panic(ret)
	}
	return ret
}

type handshake struct {
	conn   io.ReadWriteCloser
	s      big.Int
	initer bool
	skey   []byte

	writeMu    sync.Mutex
	writes     [][]byte
	writeErr   error
	writeCond  sync.Cond
	writeClose bool

	writerMu   sync.Mutex
	writerCond sync.Cond
	writerDone bool
}

func (h *handshake) finishWriting() (err error) {
	h.writeMu.Lock()
	h.writeClose = true
	h.writeCond.Broadcast()
	err = h.writeErr
	h.writeMu.Unlock()

	h.writerMu.Lock()
	for !h.writerDone {
		h.writerCond.Wait()
	}
	h.writerMu.Unlock()
	return

}

func (h *handshake) writer() {
	defer func() {
		h.writerMu.Lock()
		h.writerDone = true
		h.writerCond.Broadcast()
		h.writerMu.Unlock()
	}()
	for {
		h.writeMu.Lock()
		for {
			if len(h.writes) != 0 {
				break
			}
			if h.writeClose {
				h.writeMu.Unlock()
				return
			}
			h.writeCond.Wait()
		}
		b := h.writes[0]
		h.writes = h.writes[1:]
		h.writeMu.Unlock()
		_, err := h.conn.Write(b)
		if err != nil {
			h.writeMu.Lock()
			h.writeErr = err
			h.writeMu.Unlock()
			return
		}
	}
}

func (h *handshake) postWrite(b []byte) error {
	h.writeMu.Lock()
	defer h.writeMu.Unlock()
	if h.writeErr != nil {
		return h.writeErr
	}
	h.writes = append(h.writes, b)
	h.writeCond.Signal()
	return nil
}

func xor(dst, src []byte) (ret []byte) {
	max := len(dst)
	if max > len(src) {
		max = len(src)
	}
	ret = make([]byte, 0, max)
	for i := range iter.N(max) {
		ret = append(ret, dst[i]^src[i])
	}
	return
}

type cryptoNegotiation struct {
	VC     [8]byte
	Method uint32
	PadLen uint16
	IA     []byte
}

func (me *cryptoNegotiation) UnmarshalReader(r io.Reader) (err error) {
	_, err = io.ReadFull(r, me.VC[:])
	if err != nil {
		return
	}
	err = binary.Read(r, binary.BigEndian, &me.Method)
	if err != nil {
		return
	}
	err = binary.Read(r, binary.BigEndian, &me.PadLen)
	if err != nil {
		return
	}
	log.Print(me.PadLen)
	_, err = io.CopyN(ioutil.Discard, r, int64(me.PadLen))
	return
}

func (me *cryptoNegotiation) MarshalWriter(w io.Writer) (err error) {
	_, err = w.Write(me.VC[:])
	if err != nil {
		return
	}
	err = binary.Write(w, binary.BigEndian, me.Method)
	if err != nil {
		return
	}
	err = binary.Write(w, binary.BigEndian, me.PadLen)
	if err != nil {
		return
	}
	_, err = w.Write(make([]byte, me.PadLen))
	return
}

// Looking for b at the end of a.
func suffixMatchLen(a, b []byte) int {
	if len(b) > len(a) {
		b = b[:len(a)]
	}
	// i is how much of b to try to match
	for i := len(b); i > 0; i-- {
		// j is how many chars we've compared
		j := 0
		for ; j < i; j++ {
			if b[i-1-j] != a[len(a)-1-j] {
				goto shorter
			}
		}
		return j
	shorter:
	}
	return 0
}

func readUntil(r io.Reader, b []byte) error {
	log.Println("read until", b)
	b1 := make([]byte, len(b))
	i := 0
	for {
		_, err := io.ReadFull(r, b1[i:])
		if err != nil {
			return err
		}
		i = suffixMatchLen(b1, b)
		if i == len(b) {
			break
		}
		if copy(b1, b1[len(b1)-i:]) != i {
			panic("wat")
		}
	}
	return nil
}

func (h *handshake) Do() (ret io.ReadWriteCloser, err error) {
	err = h.establishS()
	if err != nil {
		return
	}
	pad := make([]byte, newPadLen())
	io.ReadFull(rand.Reader, pad)
	err = h.postWrite(pad)
	if err != nil {
		return
	}
	if h.initer {
		h.postWrite(hash(req1, h.s.Bytes()))
		h.postWrite(xor(hash(req2, h.skey), hash(req3, h.s.Bytes())))
		buf := &bytes.Buffer{}
		err = (&cryptoNegotiation{
			Method: cryptoMethodRC4,
			PadLen: uint16(newPadLen()),
		}).MarshalWriter(buf)
		if err != nil {
			return
		}
		e := newEncrypt(true, h.s.Bytes(), h.skey)
		be := make([]byte, buf.Len())
		e.XORKeyStream(be, buf.Bytes())
		h.postWrite(be)
		bC := newEncrypt(false, h.s.Bytes(), h.skey)
		var eVC [8]byte
		bC.XORKeyStream(eVC[:], make([]byte, 8))
		log.Print(eVC)
		// Read until the all zero VC.
		err = readUntil(h.conn, eVC[:])
		if err != nil {
			err = fmt.Errorf("error reading until VC: %s", err)
			return
		}
		var cn cryptoNegotiation
		r := &cipherReader{bC, h.conn}
		err = cn.UnmarshalReader(io.MultiReader(bytes.NewReader(make([]byte, 8)), r))
		log.Printf("initer got %v", cn)
		if err != nil {
			err = fmt.Errorf("error reading crypto negotiation: %s", err)
			return
		}
	} else {
		err = readUntil(h.conn, hash(req1, h.s.Bytes()))
		if err != nil {
			return
		}
		var b [20]byte
		_, err = io.ReadFull(h.conn, b[:])
		if err != nil {
			return
		}
		if !bytes.Equal(xor(hash(req2, h.skey), hash(req3, h.s.Bytes())), b[:]) {
			err = errors.New("skey doesn't match")
			return
		}
		var cn cryptoNegotiation
		r := newCipherReader(newEncrypt(true, h.s.Bytes(), h.skey), h.conn)
		err = cn.UnmarshalReader(r)
		if err != nil {
			return
		}
		log.Printf("receiver got %v", cn)
		if cn.Method&cryptoMethodRC4 == 0 {
			err = errors.New("no supported crypto methods were provided")
			return
		}
		buf := &bytes.Buffer{}
		w := newCipherWriter(newEncrypt(false, h.s.Bytes(), h.skey), buf)
		err = (&cryptoNegotiation{
			Method: cryptoMethodRC4,
			PadLen: uint16(newPadLen()),
		}).MarshalWriter(w)
		if err != nil {
			return
		}
		log.Println("encrypted VC", buf.Bytes()[:8])
		err = h.postWrite(buf.Bytes())
		if err != nil {
			return
		}
	}
	err = h.finishWriting()
	if err != nil {
		return
	}
	ret = h.conn
	return
}

func Handshake(rw io.ReadWriteCloser, initer bool, skey []byte) (ret io.ReadWriteCloser, err error) {
	h := handshake{
		conn:   rw,
		initer: initer,
		skey:   skey,
	}
	h.writeCond.L = &h.writeMu
	h.writerCond.L = &h.writerMu
	go h.writer()
	return h.Do()
}
