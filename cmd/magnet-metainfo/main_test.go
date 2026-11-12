package main

import (
	"testing"

	"github.com/go-quicktest/qt"
)

func TestMetainfoFileName(t *testing.T) {
	const ih = "0123456789abcdef0123456789abcdef01234567"
	for _, tc := range []struct {
		name, want string
	}{
		{"ubuntu.iso", "ubuntu.iso.torrent"},
		{"", ih + ".torrent"},
		{".", ih + ".torrent"},
		{"..", ih + ".torrent"},
		{"../x", ih + ".torrent"},
		{"/etc/x", ih + ".torrent"},
		{"a/b", ih + ".torrent"},
		{`a\b`, ih + ".torrent"},
	} {
		qt.Check(t, qt.Equals(metainfoFileName(tc.name, ih), tc.want), qt.Commentf("%q", tc.name))
	}
}
