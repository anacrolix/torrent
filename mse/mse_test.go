package mse

import (
	"bytes"
	"crypto/rand"
	"crypto/rc4"
	"io"
	"io/ioutil"
	"net"
	"sync"
	"testing"

	_ "github.com/anacrolix/envpprof"
	"github.com/bradfitz/iter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sliceIter(skeys [][]byte) SecretKeyIter {
	return func(callback func([]byte) bool) {
		for _, sk := range skeys {
			if !callback(sk) {
				break
			}
		}
	}
}

func TestReadUntil(t *testing.T) {
	test := func(data, until string, leftover int, expectedErr error) {
		r := bytes.NewReader([]byte(data))
		err := readUntil(r, []byte(until))
		if err != expectedErr {
			t.Fatal(err)
		}
		if r.Len() != leftover {
			t.Fatal(r.Len())
		}
	}
	test("feakjfeafeafegbaabc00", "abc", 2, nil)
	test("feakjfeafeafegbaadc00", "abc", 0, io.EOF)
}

func TestSuffixMatchLen(t *testing.T) {
	test := func(a, b string, expected int) {
		actual := suffixMatchLen([]byte(a), []byte(b))
		if actual != expected {
			t.Fatalf("expected %d, got %d for %q and %q", expected, actual, a, b)
		}
	}
	test("hello", "world", 0)
	test("hello", "lo", 2)
	test("hello", "llo", 3)
	test("hello", "hell", 0)
	test("hello", "helloooo!", 5)
	test("hello", "lol!", 2)
	test("hello", "mondo", 0)
	test("mongo", "webscale", 0)
	test("sup", "person", 1)
}

func handshakeTest(t testing.TB, ia []byte, aData, bData string, cryptoProvides CryptoMethod, cryptoSelect CryptoSelector) {
	a, b := net.Pipe()
	wg := sync.WaitGroup{}
	wg.Add(2)
	go func() {
		defer wg.Done()
		a, cm, err := InitiateHandshake(a, []byte("yep"), ia, cryptoProvides)
		require.NoError(t, err)
		assert.Equal(t, cryptoSelect(cryptoProvides), cm)
		go a.Write([]byte(aData))

		var msg [20]byte
		n, _ := a.Read(msg[:])
		if n != len(bData) {
			t.FailNow()
		}
		// t.Log(string(msg[:n]))
	}()
	go func() {
		defer wg.Done()
		b, cm, err := ReceiveHandshake(b, sliceIter([][]byte{[]byte("nope"), []byte("yep"), []byte("maybe")}), cryptoSelect)
		require.NoError(t, err)
		assert.Equal(t, cryptoSelect(cryptoProvides), cm)
		go b.Write([]byte(bData))
		// Need to be exact here, as there are several reads, and net.Pipe is
		// most synchronous.
		msg := make([]byte, len(ia)+len(aData))
		n, _ := io.ReadFull(b, msg[:])
		if n != len(msg) {
			t.FailNow()
		}
		// t.Log(string(msg[:n]))
	}()
	wg.Wait()
	a.Close()
	b.Close()
}

func allHandshakeTests(t testing.TB, provides CryptoMethod, selector CryptoSelector) {
	handshakeTest(t, []byte("jump the gun, "), "hello world", "yo dawg", provides, selector)
	handshakeTest(t, nil, "hello world", "yo dawg", provides, selector)
	handshakeTest(t, []byte{}, "hello world", "yo dawg", provides, selector)
}

func TestHandshakeDefault(t *testing.T) {
	allHandshakeTests(t, AllSupportedCrypto, DefaultCryptoSelector)
	t.Logf("crypto provides encountered: %s", cryptoProvidesCount)
}

func TestHandshakeSelectPlaintext(t *testing.T) {
	allHandshakeTests(t, AllSupportedCrypto, func(CryptoMethod) CryptoMethod { return CryptoMethodPlaintext })
}

func BenchmarkHandshakeDefault(b *testing.B) {
	for range iter.N(b.N) {
		allHandshakeTests(b, AllSupportedCrypto, DefaultCryptoSelector)
	}
}

type trackReader struct {
	r io.Reader
	n int64
}

func (tr *trackReader) Read(b []byte) (n int, err error) {
	n, err = tr.r.Read(b)
	tr.n += int64(n)
	return
}

func TestReceiveRandomData(t *testing.T) {
	tr := trackReader{rand.Reader, 0}
	_, _, err := ReceiveHandshake(readWriter{&tr, ioutil.Discard}, nil, DefaultCryptoSelector)
	// No skey matches
	require.Error(t, err)
	// Establishing S, and then reading the maximum padding for giving up on
	// synchronizing.
	require.EqualValues(t, 96+532, tr.n)
}

func fillRand(t testing.TB, bs ...[]byte) {
	for _, b := range bs {
		_, err := rand.Read(b)
		require.NoError(t, err)
	}
}

func readAndWrite(rw io.ReadWriter, r []byte, w []byte) error {
	var wg sync.WaitGroup
	wg.Add(1)
	var wErr error
	go func() {
		defer wg.Done()
		_, wErr = rw.Write(w)
	}()
	_, err := io.ReadFull(rw, r)
	if err != nil {
		return err
	}
	wg.Wait()
	return wErr
}

func benchmarkStream(t *testing.B, crypto CryptoMethod) {
	ia := make([]byte, 0x1000)
	a := make([]byte, 1<<20)
	b := make([]byte, 1<<20)
	fillRand(t, ia, a, b)
	t.StopTimer()
	t.SetBytes(int64(len(ia) + len(a) + len(b)))
	t.ResetTimer()
	for range iter.N(t.N) {
		ac, bc := net.Pipe()
		ar := make([]byte, len(b))
		br := make([]byte, len(ia)+len(a))
		t.StartTimer()
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer ac.Close()
			defer wg.Done()
			rw, _, err := InitiateHandshake(ac, []byte("cats"), ia, crypto)
			require.NoError(t, err)
			require.NoError(t, readAndWrite(rw, ar, a))
		}()
		func() {
			defer bc.Close()
			rw, _, err := ReceiveHandshake(bc, sliceIter([][]byte{[]byte("cats")}), func(CryptoMethod) CryptoMethod { return crypto })
			require.NoError(t, err)
			require.NoError(t, readAndWrite(rw, br, b))
		}()
		t.StopTimer()
		if !bytes.Equal(ar, b) {
			t.Fatalf("A read the wrong bytes")
		}
		if !bytes.Equal(br[:len(ia)], ia) {
			t.Fatalf("B read the wrong IA")
		}
		if !bytes.Equal(br[len(ia):], a) {
			t.Fatalf("B read the wrong A")
		}
		// require.Equal(t, b, ar)
		// require.Equal(t, ia, br[:len(ia)])
		// require.Equal(t, a, br[len(ia):])
	}
}

func BenchmarkStreamRC4(t *testing.B) {
	benchmarkStream(t, CryptoMethodRC4)
}

func BenchmarkStreamPlaintext(t *testing.B) {
	benchmarkStream(t, CryptoMethodPlaintext)
}

func BenchmarkPipeRC4(t *testing.B) {
	key := make([]byte, 20)
	n, _ := rand.Read(key)
	require.Equal(t, len(key), n)
	var buf bytes.Buffer
	c, err := rc4.NewCipher(key)
	require.NoError(t, err)
	r := cipherReader{
		c: c,
		r: &buf,
	}
	c, err = rc4.NewCipher(key)
	require.NoError(t, err)
	w := cipherWriter{
		c: c,
		w: &buf,
	}
	a := make([]byte, 0x1000)
	n, _ = io.ReadFull(rand.Reader, a)
	require.Equal(t, len(a), n)
	b := make([]byte, len(a))
	t.SetBytes(int64(len(a)))
	t.ResetTimer()
	for range iter.N(t.N) {
		n, _ = w.Write(a)
		if n != len(a) {
			t.FailNow()
		}
		n, _ = r.Read(b)
		if n != len(b) {
			t.FailNow()
		}
		if !bytes.Equal(a, b) {
			t.FailNow()
		}
	}
}
