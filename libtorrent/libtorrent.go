package libtorrent

// #include <stdlib.h>
import "C"

import (
	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"sync"
)

// Create
//
// Create libtorrent object
//
//export Create
func Create() bool {
	var err error
	client, err = torrent.NewClient(&clientConfig)
	if err != nil {
		return false
	}
	return true
}

// AddMagnet
//
// Add magnet link to download list
//
//export AddMagnet
func AddMagnet(magnet string) int {
	t, err := client.AddMagnet(magnet)
	if err != nil {
		return -1
	}
	return register(t)
}

// AddTorrent
//
// Add torrent to download list
//
//export AddTorrent
func AddTorrent(file string) int {
	metaInfo, err := metainfo.LoadFromFile(file)
	if err != nil {
		return -1
	}
	t, err := client.AddTorrent(metaInfo)
	if err != nil {
		return -1
	}
	return register(t)
}

// SaveTorrent
//
// Save torrent to state file
//
//export SaveTorrent
func SaveTorrent(i int) []byte {
	return nil
}

// LoadTorrent
//
// Load torrent from saved state file
//
//export LoadTorrent
func LoadTorrent(buf []byte) {
}

//export StartTorrent
func StartTorrent(i int) {
	t := torrents[i]

	client.StartTorrent(t)

	go func() {
		<-t.GotInfo()
		t.DownloadAll()
	}()
}

//export StopTorrent
func StopTorrent(i int) {
	t := torrents[i]
	client.StopTorrent(t)
}

// CheckTorrent
//
// Check torrent file consisteny (pices hases) on a disk.
//
//export CheckTorrent
func CheckTorrent(i int) {
	t := torrents[i]
	client.CheckTorrent(t)
}

//export RemoveTorrent
func RemoveTorrent(i int) {
	t := torrents[i]
	t.Drop()
	unregister(i)
}

// TorrentFiles
//
// return torrent files array
//
//export TorrentFiles
func TorrentFiles(i int) *C.char {
	return nil
}

// TorrentFileRename
//
// To implement this we need to keep two Metainfo one for network operations,
// and second for local file storage.
//
//export TorrentFileRename
func TorrentFileRename(i int, f int, n *C.char) {
}

// Total size of torrent
//
//export TorrentTotalSize
func TorrentTotalSize(i int) int64 {
	return 0
}

// Current (downloaded) size
//
//export TorrentCurrentSize
func TorrentCurrentSize(i int) int64 {
	return 0
}

// Error()
//
// Must call free() on buffer pointer.
//
//export Error
func Error() *C.char {
	return C.CString(err.Error())
}

//export Free
func Free(p *C.char) {
	C.free(p)
}

//export Close
func Close() {
	if client != nil {
		client.Close()
		client = nil
	}
}

// protected

var clientConfig torrent.Config
var client *torrent.Client
var err error
var torrents = make(map[int]*torrent.Torrent)
var index int
var mu sync.Mutex

func register(t *torrent.Torrent) int {
	mu.Lock()
	defer mu.Unlock()

	index++
	for torrents[index] != nil {
		index++
	}
	torrents[index] = t
	return index
}

func unregister(i int) {
	mu.Lock()
	defer mu.Unlock()

	delete(torrents, i)
}
