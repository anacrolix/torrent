//go:build go1.18
// +build go1.18

package dht

import (
	"bytes"
	"testing"
)

func FuzzHashTuple(f *testing.F) {
	f.Fuzz(func(t *testing.T, a, b []byte) {
		if HashTuple(a, b) == HashTuple(append(a, b...)) {
			t.FailNow()
		}
		if HashTuple(a) == HashTuple() {
			t.FailNow()
		}
		if HashTuple(b) == HashTuple() {
			t.FailNow()
		}
		if bytes.Compare(a, b) != 0 {
			if HashTuple(a) == HashTuple(b) {
				t.FailNow()
			}
			if HashTuple(b, a) == HashTuple(a, b) {
				t.FailNow()
			}
		}
	})
}
