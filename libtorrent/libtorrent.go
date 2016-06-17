package torrent

import "C"

import (
	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"sync"
)

//export Create
func Create() bool {
	var err error
	client, err = torrent.NewClient(&clientConfig)
	if err != nil {
		return false
	}
	return true
}

//export AddMagnet
func AddMagnet(magnet string) int {
	t, err := client.AddMagnet(magnet)
	if err != nil {
		return -1
	}
	return register(t)
}

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

//export StartTorrent
func StartTorrent(i int) {
	t := torrents[i]
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

//export Error
func Error() *C.char {
	// must call free() on buffer pointer
	return C.CString(err.Error())
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
