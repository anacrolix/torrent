package webseed

import (
	"fmt"
	"net/http"
	"path"
	"strings"

	"github.com/anacrolix/torrent/metainfo"
)

// Creates a request per BEP 19.
func NewRequest(url string, fileIndex int, info *metainfo.Info, offset, length int64) (*http.Request, error) {
	fileInfo := info.UpvertedFiles()[fileIndex]
	if strings.HasSuffix(url, "/") {
		url += path.Join(append([]string{info.Name}, fileInfo.Path...)...)
	}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if offset != 0 || length != fileInfo.Length {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, offset+length-1))
	}
	return req, nil
}
