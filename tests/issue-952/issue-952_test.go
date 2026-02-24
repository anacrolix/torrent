package issue_952

import (
	"testing"

	qt "github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/types/infohash"
)

type scrapeResponse struct {
	Files map[metainfo.Hash]scrapeResponseFile `bencode:"files"`
}

type scrapeResponseFile struct {
	Complete   int `bencode:"complete"`
	Downloaded int `bencode:"downloaded"`
	Incomplete int `bencode:"incomplete"`
}

// This tests unmarshalling to a map with a non-string dict key.
func TestUnmarshalStringToByteArray(t *testing.T) {
	var s scrapeResponse
	const hashStr = "\x05a~F\xfd{c\xd1`\xb8\xd9\x89\xceM\xb9t\x1d\\\x0b\xded"
	err := bencode.Unmarshal([]byte("d5:filesd20:\x05a~F\xfd{c\xd1`\xb8\xd9\x89\xceM\xb9t\x1d\\\x0b\xded9:completedi1e10:downloadedi1eeee"), &s)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.HasLen(s.Files, 1))
	file, ok := s.Files[(infohash.T)([]byte(hashStr))]
	qt.Assert(t, qt.IsTrue(ok))
	qt.Check(t, qt.Equals(file, scrapeResponseFile{
		// Note that complete is misspelled in the example. I don't know why.
		Complete:   0,
		Downloaded: 1,
		Incomplete: 0,
	}))

}
