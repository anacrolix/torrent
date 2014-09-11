package torrent

import (
	"encoding/base32"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
)

type Magnet struct {
	InfoHash    [20]byte
	Trackers    []string
	DisplayName string
}

const xtPrefix = "urn:btih:"

func ParseMagnetURI(uri string) (m Magnet, err error) {
	u, err := url.Parse(uri)
	if err != nil {
		err = fmt.Errorf("error parsing uri: %s", err)
		return
	}
	if u.Scheme != "magnet" {
		err = fmt.Errorf("unexpected scheme: %q", u.Scheme)
		return
	}
	xt := u.Query().Get("xt")
	if !strings.HasPrefix(xt, xtPrefix) {
		err = fmt.Errorf("bad xt parameter")
		return
	}
	xt = xt[len(xtPrefix):]
	decode := func() func(dst, src []byte) (int, error) {
		switch len(xt) {
		case 40:
			return hex.Decode
		case 32:
			return base32.StdEncoding.Decode
		default:
			return nil
		}
	}()
	if decode == nil {
		err = fmt.Errorf("unhandled xt parameter encoding")
		return
	}
	n, err := decode(m.InfoHash[:], []byte(xt))
	if err != nil {
		err = fmt.Errorf("error decoding xt: %s", err)
		return
	}
	if n != 20 {
		panic(n)
	}
	m.DisplayName = u.Query().Get("dn")
	m.Trackers = u.Query()["tr"]
	return
}
