package libtorrent

// #include <stdlib.h>
import "C"

import (
	"bufio"
	"bytes"
	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"sync"
)

//export CreateTorrent
func CreateTorrent(path string, announs []string) []byte {
	mi := metainfo.MetaInfo{}
	for _, a := range announs {
		mi.AnnounceList = append(mi.AnnounceList, []string{a})
	}
	mi.SetDefaults()
	err = mi.Info.BuildFromFilePath(path)
	if err != nil {
		return nil
	}
	var b bytes.Buffer
	w := bufio.NewWriter(&b)
	err = mi.Write(w)
	if err != nil {
		return nil
	}
	err = w.Flush()
	if err != nil {
		return nil
	}
	return b.Bytes()
}

// Create
//
// Create libtorrent object
//
//export Create
func Create() bool {
	client, err = torrent.NewClient(&clientConfig)
	if err != nil {
		return false
	}
	return true
}

type BytesInfo struct {
	Downloaded int64
	Uploaded   int64
}

func Stats() *BytesInfo {
	d, u := client.Stats()
	return &BytesInfo{d, u}
}

// Get Torrent Count
//
//export Count
func Count() int {
	return len(torrents)
}

// AddMagnet
//
// Add magnet link to download list
//
//export AddMagnet
func AddMagnet(magnet string) int {
	var t *torrent.Torrent
	t, err = client.AddMagnet(magnet)
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
	var metaInfo *metainfo.MetaInfo
	metaInfo, err = metainfo.LoadFromFile(file)
	if err != nil {
		return -1
	}
	var t *torrent.Torrent
	t, err = client.AddTorrent(metaInfo)
	if err != nil {
		return -1
	}
	return register(t)
}

// Get Torrent file from runtime torrent
//
//export GetTorrent
func GetTorrent(i int) []byte {
	t := torrents[i]

	var buf bytes.Buffer
	w := bufio.NewWriter(&buf)
	err = t.Metainfo().Write(w)
	if err != nil {
		return nil
	}
	err = w.Flush()
	if err != nil {
		return nil
	}
	return buf.Bytes()
}

// SaveTorrent
//
// Every torrent application restarts it require to check files consistency. To
// avoid this, and save machine time we need to store torrents runtime states
// completed pieces and other information externaly.
//
// Save runtime torrent data to state file
//
//export SaveTorrent
func SaveTorrent(i int) []byte {
	return nil
}

// LoadTorrent
//
// Load runtime torrent data from saved state file
//
//export LoadTorrent
func LoadTorrent(buf []byte) int {
	return -1
}

// Separate load / create torrent from network activity.
//
// Start announce torrent, seed/download
//
//export StartTorrent
func StartTorrent(i int) {
	t := torrents[i]

	client.StartTorrent(t)

	go func() {
		<-t.GotInfo()
		t.DownloadAll()
	}()
}

// Stop torrent from announce, check, seed, download
//
//export StopTorrent
func StopTorrent(i int) {
	t := torrents[i]
	if client.ActiveTorrent(t) {
		t.Drop()
	}
}

// CheckTorrent
//
// Check torrent file consisteny (pices hases) on a disk. Pause torrent if
// downloading, resume after.
//
//export CheckTorrent
func CheckTorrent(i int) {
	t := torrents[i]
	client.CheckTorrent(t)
}

// Remote torrent for library
//
//export RemoveTorrent
func RemoveTorrent(i int) {
	t := torrents[i]
	if client.ActiveTorrent(t) {
		t.Drop()
	}
	unregister(i)
}

//export Error
func Error() string {
	if err != nil {
		return err.Error()
	}
	return ""
}

//export Close
func Close() {
	if client != nil {
		client.Close()
		client = nil
	}
}

//
// Torrent* methods
//

// Get Magnet from runtime torrent.
//
//export TorrentMagnet
func TorrentMagnet(i int) string {
	t := torrents[i]
	return t.Metainfo().Magnet().String()
}

func TorrentMetainfo(i int) *metainfo.MetaInfo {
	t := torrents[i]
	return t.Metainfo()
}

//export TorrentHash
func TorrentHash(i int) string {
	t := torrents[i]
	h := t.InfoHash()
	return h.AsString()
}

//export TorrentName
func TorrentName(i int) string {
	t := torrents[i]
	return t.Name()
}

const (
	StatusPaused      int32 = 0
	StatusDownloading int32 = 1
	StatusSeeding     int32 = 2
	StatusQueued      int32 = 3
)

//export TorrentStatus
func TorrentStatus(i int) int32 {
	t := torrents[i]

	if t.Info() != nil {
		if t.BytesCompleted() < t.Length() {
			if client.ActiveTorrent(t) {
				return StatusDownloading
			} else {
				return StatusPaused
			}
		} else {
			if t.Seeding() {
				return StatusSeeding
			} else {
				return StatusPaused
			}
		}
	} else {
		if client.ActiveTorrent(t) {
			return StatusDownloading
		} else {
			return StatusPaused
		}
	}
}

//export TorrentBytesLength
func TorrentBytesLength(i int) int64 {
	t := torrents[i]
	return t.Length()
}

//export TorrentBytesCompleted
func TorrentBytesCompleted(i int) int64 {
	t := torrents[i]
	return t.BytesCompleted()
}

func TorrentStats(i int) *BytesInfo {
	return &BytesInfo{}
}

type File struct {
	Check          bool
	Path           string
	Length         int64
	BytesCompleted int64
}

// return torrent files array
func TorrentFiles(i int) []File {
	t := torrents[i]
	var ff []File
	for _, v := range t.Files() {
		f := File{}
		f.Check = true
		f.Path = v.Path()
		f.Length = v.Length()
		ff = append(ff, f)
	}
	return ff
}

func TorrentPeersCount(i int) int {
	t := torrents[i]
	return t.PeersCount()
}

func TorrentPeers(i int) []torrent.Peer {
	t := torrents[i]
	return t.Peers()
}

// TorrentFileRename
//
// To implement this we need to keep two Metainfo one for network operations,
// and second for local file storage.
//
//export TorrentFileRename
func TorrentFileRename(i int, f int, n string) {
}

type Tracker struct {
	// url or DHT LSD
	Addr         string
	Error        string
	LastAnnounce int
	NextAnnounce int
	LastScrape   int
	Seeders      int
	Leechers     int
	Downloaded   int
}

func TorrentTrackers(i int) []Tracker {
	return nil
}

//
// protected
//

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
