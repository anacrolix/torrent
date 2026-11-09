package metainfo

import (
	"testing"
)

// Magnet links come from users, web pages and other peers' metadata.
func FuzzParseMagnetUri(f *testing.F) {
	f.Add(exampleMagnetURI)
	f.Add("magnet:?xt=urn:btih:ZOCMZQIPFFW7OLLMIC5HUB6BPCSDEOQU")
	f.Add("magnet:?xt=urn:btih:51340689c960f0778a4387aef9b4b52fd08390cd&xs=http%3A%2F%2Fexample.com%2Ft.torrent&so=0-3,5")
	f.Fuzz(func(t *testing.T, uri string) {
		m, err := ParseMagnetUri(uri)
		if err != nil {
			return
		}
		if _, err := ParseMagnetUri(m.String()); err != nil {
			t.Fatalf("parsing %q, from %q: %v", m.String(), uri, err)
		}
	})
}

func FuzzParseMagnetV2Uri(f *testing.F) {
	f.Add(exampleMagnetURI)
	f.Add("magnet:?xt=urn:btmh:1220caf1e1c30e81cb361b9ee167c4aa64228a7fa4fa9f6105232b28ad099f3a302e&dn=bittorrent-v2-test")
	f.Add("magnet:?xt=urn:btih:631a31dd0a46257d5078c0dee4e66e26f73e42ac&xt=urn:btmh:1220d8dd32ac93357c368556af3ac1d95c9d76bd0dff6fa9833ecdac3d53134efabb&dn=bittorrent-v1-v2-hybrid-test")
	f.Fuzz(func(t *testing.T, uri string) {
		m, err := ParseMagnetV2Uri(uri)
		if err != nil {
			return
		}
		if _, err := ParseMagnetV2Uri(m.String()); err != nil {
			t.Fatalf("parsing %q, from %q: %v", m.String(), uri, err)
		}
	})
}
