package dirwatch

import (
	"bitbucket.org/anacrolix/go.torrent"
	"bufio"
	"github.com/anacrolix/libtorgo/metainfo"
	"github.com/go-fsnotify/fsnotify"
	"log"
	"os"
	"path/filepath"
)

type Change uint

const (
	Added Change = iota
	Removed
)

type Event struct {
	Magnet string
	Change
	TorrentFilePath string
	InfoHash        torrent.InfoHash
}

type Instance struct {
	w                     *fsnotify.Watcher
	dirName               string
	Events                chan Event
	torrentFileInfoHashes map[string]torrent.InfoHash
	magnetFileInfoHashes  map[string]map[torrent.InfoHash]struct{}
}

func (me *Instance) handleEvents() {
	for e := range me.w.Events {
		log.Printf("event: %s", e)
		me.processFile(e.Name)
	}
}

func (me *Instance) handleErrors() {
	for err := range me.w.Errors {
		log.Printf("error in torrent directory watcher: %s", err)
	}
}

func torrentFileInfoHash(fileName string) (ih torrent.InfoHash, ok bool) {
	mi, _ := metainfo.LoadFromFile(fileName)
	if mi == nil {
		return
	}
	if 20 != copy(ih[:], mi.Info.Hash) {
		panic(mi.Info.Hash)
	}
	ok = true
	return
}

func magnetFileURIs(name string) (uris []string, err error) {
	f, err := os.Open(name)
	if err != nil {
		return
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		uris = append(uris, scanner.Text())
	}
	err = scanner.Err()
	return
}

func (me *Instance) removeAllFileMagnets(name string) {
	for ih := range me.magnetFileInfoHashes[name] {
		me.Events <- Event{
			InfoHash: ih,
			Change:   Removed,
		}
	}
}

func (me *Instance) removeTorrent(ih torrent.InfoHash) {
	me.Events <- Event{
		InfoHash: ih,
		Change:   Removed,
	}
}

func (me *Instance) processFile(name string) {
	name = filepath.Clean(name)
	log.Print(name)
	switch filepath.Ext(name) {
	case ".torrent":
		ih, ok := me.torrentFileInfoHashes[name]
		if ok {
			me.Events <- Event{
				TorrentFilePath: name,
				Change:          Removed,
				InfoHash:        ih,
			}
		}
		delete(me.torrentFileInfoHashes, name)
		ih, ok = torrentFileInfoHash(name)
		if ok {
			me.torrentFileInfoHashes[name] = ih
			me.Events <- Event{
				TorrentFilePath: name,
				Change:          Added,
				InfoHash:        ih,
			}
		}
	case ".magnet":
		me.removeAllFileMagnets(name)
		uris, err := magnetFileURIs(name)
		if err != nil {
			log.Print(err)
			break
		}
		for _, uri := range uris {
			m, err := torrent.ParseMagnetURI(uri)
			if err != nil {
				log.Printf("bad magnet uri in magnet file: %s", err)
				continue
			}
			me.removeTorrent(m.InfoHash)
			me.Events <- Event{
				Magnet: uri,
				Change: Added,
			}
		}
	default:
		return
	}
}

func (me *Instance) addDir() (err error) {
	f, err := os.Open(me.dirName)
	if err != nil {
		return
	}
	defer f.Close()
	names, err := f.Readdirnames(-1)
	if err != nil {
		return
	}
	for _, n := range names {
		me.processFile(filepath.Join(me.dirName, n))
	}
	return
}

func New(dirName string) (i *Instance, err error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return
	}
	err = w.Add(dirName)
	if err != nil {
		w.Close()
		return
	}
	i = &Instance{
		w:                     w,
		dirName:               dirName,
		Events:                make(chan Event),
		torrentFileInfoHashes: make(map[string]torrent.InfoHash, 20),
	}
	go func() {
		i.addDir()
		go i.handleEvents()
		go i.handleErrors()
	}()
	return
}
