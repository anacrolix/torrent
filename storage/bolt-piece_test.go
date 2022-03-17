package storage_test

import (
	"testing"

	"github.com/anacrolix/torrent/storage"
	"github.com/anacrolix/torrent/test"
)

func TestBoltLeecherStorage(t *testing.T) {
	test.TestLeecherStorage(t, test.LeecherStorageTestCase{"Boltdb", storage.NewBoltDB, 0})
}
