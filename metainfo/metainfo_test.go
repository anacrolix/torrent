package metainfo

import "testing"
import "path"

func test_file(t *testing.T, filename string) {
	mi, err := LoadFromFile(filename)
	if err != nil {
		t.Fatal(err)
	}

	if len(mi.Files) == 1 {
		t.Logf("Single file: %s (length: %d)\n", mi.Name, mi.Files[0].Length)
	} else {
		t.Logf("Multiple files: %s\n", mi.Name)
		for _, f := range mi.Files {
			t.Logf(" - %s (length: %d)\n", path.Join(f.Path...), f.Length)
		}
	}

	for _, group := range mi.AnnounceList {
		for _, tracker := range group {
			t.Logf("Tracker: %s\n", tracker)
		}
	}
	for _, url := range mi.WebSeedURLs {
		t.Logf("URL: %s\n", url)
	}

}

func TestFile(t *testing.T) {
	test_file(t, "_testdata/archlinux-2011.08.19-netinstall-i686.iso.torrent")
	test_file(t, "_testdata/continuum.torrent")
}
