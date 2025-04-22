package md5x

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"hash"
	"io"
)

// digest the provided contents and return the resulting hash.
// if an error occurs during hashing then a nil value is returned.
func Digest[T string | []byte](bs ...T) hash.Hash {
	v := md5.New()

	for _, b := range bs {
		y := []byte(b)
		if n, err := v.Write(y); err != nil || n < len(y) {
			return nil
		}
	}

	return v
}

func IO(src io.Reader) (int64, hash.Hash, error) {
	v := md5.New()
	n, err := io.Copy(v, src)
	return n, v, err
}

// format md5 hash to a hex encoded string
func FormatHex(m hash.Hash) string {
	return hex.EncodeToString(m.Sum(nil))
}

// format hash to a base64 encoded string
func FormatBase64(m hash.Hash) string {
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}
