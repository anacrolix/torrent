// Package buffer mirrors the Node.JS buffer type.
package buffer

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// Buffer mirrors the Node.JS Buffer type.
type Buffer struct {
	b []byte
}

// New creates a new buffer from b
func New(b []byte) *Buffer {
	return &Buffer{b: b}
}

// From creates a new buffer from a string
func From(s string) *Buffer {
	return &Buffer{b: []byte(s)}
}

// FromHex creates a new buffer from a hex string.
func FromHex(in string) (*Buffer, error) {
	decoded, err := hex.DecodeString(in)
	if err != nil {
		return nil, fmt.Errorf("failed to decode hex: %v", err)
	}
	return &Buffer{b: decoded}, nil
}

// ToStringBase64 turns the buffer into a base64 string.
func (b *Buffer) ToStringBase64() string {
	return base64.StdEncoding.EncodeToString(b.b)
}

// ToStringLatin1 turns the buffer into a string using
// Latin-1 supplement block and C0/C1 control codes.
func (b *Buffer) ToStringLatin1() string {
	seq := []rune{}
	for _, v := range b.b {
		seq = append(seq, rune(v))
	}
	return string(seq)
}

// ToStringHex converts the buffer to a hex string
func (b *Buffer) ToStringHex() string {
	return hex.EncodeToString(b.b)
}

// RandomBytes returns securely generated random bytes.
// It will return an error if the system's secure random
// number generator fails to function correctly, in which
// case the caller should not continue.
func RandomBytes(n int) (*Buffer, error) {
	b := make([]byte, n)
	_, err := rand.Read(b)
	// Note that err == nil only if we read len(b) bytes.
	if err != nil {
		return nil, err
	}

	return New(b), nil
}
