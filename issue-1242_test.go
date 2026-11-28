package torrent

import (
	"testing"

	qt "github.com/go-quicktest/qt"
)

func TestIssue1242TorrentSpecFromMagnetUriMissingInfoHash(t *testing.T) {
	for _, tc := range []struct {
		name string
		uri  string
	}{
		{
			name: "only display name",
			uri:  "magnet:?dn=only-a-name",
		},
		{
			name: "all-zero v1 infohash",
			uri:  "magnet:?xt=urn:btih:0000000000000000000000000000000000000000",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec, err := TorrentSpecFromMagnetUri(tc.uri)
			qt.Check(t, qt.IsNotNil(err))
			qt.Check(t, qt.IsNil(spec))
		})
	}
}

func TestIssue1242ClientRejectsMissingInfoHash(t *testing.T) {
	cl, err := NewClient(TestingConfig(t))
	qt.Assert(t, qt.IsNil(err))
	defer cl.Close()

	t.Run("AddMagnet missing infohash", func(t *testing.T) {
		tor, err := cl.AddMagnet("magnet:?dn=only-a-name")
		qt.Check(t, qt.IsNotNil(err))
		qt.Check(t, qt.IsNil(tor))
	})

	t.Run("AddTorrentSpec missing infohash", func(t *testing.T) {
		tor, newTorrent, err := cl.AddTorrentSpec(&TorrentSpec{DisplayName: "only-a-name"})
		qt.Check(t, qt.IsNotNil(err))
		qt.Check(t, qt.IsFalse(newTorrent))
		qt.Check(t, qt.IsNil(tor))
	})

	t.Run("AddTorrentSpec nil", func(t *testing.T) {
		tor, newTorrent, err := cl.AddTorrentSpec(nil)
		qt.Check(t, qt.IsNotNil(err))
		qt.Check(t, qt.IsFalse(newTorrent))
		qt.Check(t, qt.IsNil(tor))
	})

	t.Run("AddTorrentSpec InfoBytes without infohash", func(t *testing.T) {
		tor, newTorrent, err := cl.AddTorrentSpec(&TorrentSpec{
			AddTorrentOpts: AddTorrentOpts{
				InfoBytes: []byte("d4:name4:teste"),
			},
		})
		qt.Check(t, qt.IsNotNil(err))
		qt.Check(t, qt.IsFalse(newTorrent))
		qt.Check(t, qt.IsNil(tor))
	})
}

func TestIssue1242ValidMagnets(t *testing.T) {
	cl, err := NewClient(TestingConfig(t))
	qt.Assert(t, qt.IsNil(err))
	defer cl.Close()

	for _, tc := range []struct {
		name      string
		uri       string
		wantV1Hex string
		wantV2Hex string
	}{
		{
			name:      "v1",
			uri:       "magnet:?xt=urn:btih:51340689c960f0778a4387aef9b4b52fd08390cd&dn=v1-test",
			wantV1Hex: "51340689c960f0778a4387aef9b4b52fd08390cd",
		},
		{
			name:      "v2 only",
			uri:       "magnet:?xt=urn:btmh:1220caf1e1c30e81cb361b9ee167c4aa64228a7fa4fa9f6105232b28ad099f3a302e&dn=bittorrent-v2-test",
			wantV2Hex: "caf1e1c30e81cb361b9ee167c4aa64228a7fa4fa9f6105232b28ad099f3a302e",
		},
		{
			name:      "hybrid",
			uri:       "magnet:?xt=urn:btih:631a31dd0a46257d5078c0dee4e66e26f73e42ac&xt=urn:btmh:1220d8dd32ac93357c368556af3ac1d95c9d76bd0dff6fa9833ecdac3d53134efabb&dn=bittorrent-v1-v2-hybrid-test",
			wantV1Hex: "631a31dd0a46257d5078c0dee4e66e26f73e42ac",
			wantV2Hex: "d8dd32ac93357c368556af3ac1d95c9d76bd0dff6fa9833ecdac3d53134efabb",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec, err := TorrentSpecFromMagnetUri(tc.uri)
			qt.Assert(t, qt.IsNil(err))
			qt.Assert(t, qt.IsNotNil(spec))
			if tc.wantV1Hex != "" {
				qt.Check(t, qt.Equals(spec.InfoHash.HexString(), tc.wantV1Hex))
			} else {
				qt.Check(t, qt.IsTrue(spec.InfoHash.IsZero()))
			}
			if tc.wantV2Hex != "" {
				qt.Assert(t, qt.IsTrue(spec.InfoHashV2.Ok))
				qt.Check(t, qt.Equals(spec.InfoHashV2.Value.HexString(), tc.wantV2Hex))
			} else {
				qt.Check(t, qt.IsFalse(spec.InfoHashV2.Ok))
			}

			tor, newTorrent, err := cl.AddTorrentSpec(spec)
			qt.Assert(t, qt.IsNil(err))
			qt.Check(t, qt.IsTrue(newTorrent))
			qt.Assert(t, qt.IsNotNil(tor))
			qt.Check(t, qt.Equals(tor.infoHash.Ok, tc.wantV1Hex != ""))
			qt.Check(t, qt.Equals(tor.infoHashV2.Ok, tc.wantV2Hex != ""))
			tor.Drop()

			tor, err = cl.AddMagnet(tc.uri)
			qt.Assert(t, qt.IsNil(err))
			qt.Assert(t, qt.IsNotNil(tor))
			qt.Check(t, qt.Equals(tor.infoHash.Ok, tc.wantV1Hex != ""))
			qt.Check(t, qt.Equals(tor.infoHashV2.Ok, tc.wantV2Hex != ""))
			tor.Drop()
		})
	}
}
