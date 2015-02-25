package torrent

import (
	"encoding/hex"
	"testing"
)

// Converting from our Magnet type to URL string.
func TestMagnetString(t *testing.T) {
	m := Magnet{
		DisplayName: "Shit Movie (1985) 1337p - Eru",
		Trackers: []string{
			"http://http.was.great!",
			"udp://anti.piracy.honeypot:6969",
		},
	}
	hex.Decode(m.InfoHash[:], []byte("51340689c960f0778a4387aef9b4b52fd08390cd"))
	s := m.String()
	const e = `magnet:?xt=urn:btih:51340689c960f0778a4387aef9b4b52fd08390cd&dn=Shit+Movie+%281985%29+1337p+-+Eru&tr=http%3A%2F%2Fhttp.was.great%21&tr=udp%3A%2F%2Fanti.piracy.honeypot%3A6969`
	if s != e {
		t.Fatalf("\nexpected:\n\t%q\nactual\n\t%q", e, s)
	}
}
