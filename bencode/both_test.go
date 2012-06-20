package bencode

import "testing"
import "bytes"
import "io/ioutil"
import "time"

func load_file(name string, t *testing.T) []byte {
	data, err := ioutil.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestBothInterface(t *testing.T) {
	data1 := load_file("_testdata/archlinux-2011.08.19-netinstall-i686.iso.torrent", t)
	var iface interface{}

	err := Unmarshal(data1, &iface)
	if err != nil {
		t.Fatal(err)
	}

	data2, err := Marshal(iface)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(data1, data2) {
		t.Fatalf("equality expected\n")
	}
}

type torrent_file struct {
	Info struct {
		Name        string `bencode:"name"`
		Length      int64  `bencode:"length"`
		MD5Sum      string `bencode:"md5sum,omitempty"`
		PieceLength int64  `bencode:"piece length"`
		Pieces      string `bencode:"pieces"`
		Private     bool   `bencode:"private,omitempty"`
	} `bencode:"info"`

	Announce     string      `bencode:"announce"`
	AnnounceList [][]string  `bencode:"announce-list,omitempty"`
	CreationDate int64       `bencode:"creation date,omitempty"`
	Comment      string      `bencode:"comment,omitempty"`
	CreatedBy    string      `bencode:"created by,omitempty"`
	URLList      interface{} `bencode:"url-list,omitempty"`
}

func TestBoth(t *testing.T) {
	data1 := load_file("_testdata/archlinux-2011.08.19-netinstall-i686.iso.torrent", t)
	var f torrent_file

	err := Unmarshal(data1, &f)
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("Name: %s\n", f.Info.Name)
	t.Logf("Length: %v bytes\n", f.Info.Length)
	t.Logf("Announce: %s\n", f.Announce)
	t.Logf("CreationDate: %s\n", time.Unix(f.CreationDate, 0).String())
	t.Logf("CreatedBy: %s\n", f.CreatedBy)
	t.Logf("Comment: %s\n", f.Comment)

	data2, err := Marshal(&f)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(data1, data2) {
		t.Fatalf("equality expected")
	}
}
