package torrent

import "testing"
import "path"

func test_file(t *testing.T, filename string) {
	f, err := LoadFromFile(filename)
	if err != nil {
		t.Fatal(err)
	}

	switch info := f.Info.(type) {
	case SingleFile:
		t.Logf("Single file: %s (length: %d)\n", info.Name, info.Length)
	case MultiFile:
		t.Logf("Multiple files: %s\n", info.Name)
		for _, f := range info.Files {
			t.Logf(" - %s (length: %d)\n", path.Join(f.Path...), f.Length)
		}
	}

	for _, group := range f.AnnounceList {
		for _, tracker := range group {
			t.Logf("Tracker: %s\n", tracker)
		}
	}
	for _, url := range f.URLList {
		t.Logf("URL: %s\n", url)
	}

}

func TestFile(t *testing.T) {
	test_file(t, "_testdata/archlinux-2011.08.19-netinstall-i686.iso.torrent")
	test_file(t, "_testdata/continuum.torrent")
}
