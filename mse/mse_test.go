package mse

import (
	"bytes"
	"io"
	"net"
	"sync"

	"testing"
)

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

func TestHandshake(t *testing.T) {
	a, b := net.Pipe()
	wg := sync.WaitGroup{}
	wg.Add(2)
	go func() {
		defer wg.Done()
		a, err := Handshake(a, true, []byte("yep"))
		if err != nil {
			t.Fatal(err)
			return
		}
		a.Close()
	}()
	go func() {
		defer wg.Done()
		b, err := Handshake(b, false, []byte("yep"))
		if err != nil {
			t.Fatal(err)
			return
		}
		b.Close()
	}()
	wg.Wait()
}
