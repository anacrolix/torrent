package torrent

import (
	"bufio"
	"bytes"
	"net/http"
	"testing"
)

func TestMultiInfohash(t *testing.T) {
	html := `BT-SEARCH * HTTP/1.1
Host: 239.192.152.143:6771
Port: 3333
Infohash: 123123
Infohash: 2222
cookie: name=value


`
	req, err := http.ReadRequest(bufio.NewReader(bytes.NewReader([]byte(html))))
	if err != nil {
		t.Error("receiver", err)
		return
	}
	var ihs []string = req.Header[http.CanonicalHeaderKey("Infohash")]
	if ihs == nil {
		t.Error("receiver", "No Infohash")
		return
	}
	for _, ih := range ihs {
		t.Log(ih)
	}
}
