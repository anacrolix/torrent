package util

import (
	"bytes"
	"testing"
)

func TestCopyToArray(t *testing.T) {
	var arr [3]byte
	bb := []byte{1, 2, 3}
	CopyExact(&arr, bb)
	if !bytes.Equal(arr[:], bb) {
		t.FailNow()
	}
}

func TestCopyToSlicedArray(t *testing.T) {
	var arr [5]byte
	CopyExact(arr[:], "hello")
	if !bytes.Equal(arr[:], []byte("hello")) {
		t.FailNow()
	}
}

func TestCopyDestNotAddr(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.FailNow()
		}
		t.Log(r)
	}()
	var arr [3]byte
	CopyExact(arr, "nope")
}

func TestCopyLenMismatch(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.FailNow()
		}
		t.Log(r)
	}()
	CopyExact(make([]byte, 2), "abc")
}
