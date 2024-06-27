package webseed

import (
	"context"
	"testing"
)

func TestAlloc(t *testing.T) {
	buff, err := bufPool.get(context.Background(), 2097152)

	if err != nil {
		t.Fatal(err)
	}

	err = buff.Close()

	if err != nil {
		t.Fatal(err)
	}

	buff, err = bufPool.get(context.Background(), 2097152)

	if err != nil {
		t.Fatal(err)
	}

	input := [2097152]byte{}
	buff.Write(input[:])

	err = buff.Close()

	if err != nil {
		t.Fatal(err)
	}
}
