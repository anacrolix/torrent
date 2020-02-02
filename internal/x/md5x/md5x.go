package md5x

import (
	"crypto/md5"
	"io"
)

// Stream creates a md5 digest of the given reader, if an error
// occurrs and empty digest is returned.
func Stream(d io.Reader) []byte {
	digest := md5.New()
	if _, err := io.Copy(digest, d); err != nil {
		return []byte(nil)
	}

	return digest.Sum(nil)
}
