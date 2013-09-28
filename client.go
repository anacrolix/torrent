package torrent

import (
	"bitbucket.org/anacrolix/go.torrent/peer_protocol"
	"crypto"
	"crypto/rand"
	"errors"
	metainfo "github.com/nsf/libtorgo/torrent"
	"io"
	"launchpad.net/gommap"
	"log"
	"net"
	"os"
	"path/filepath"
)

const (
	PieceHash = crypto.SHA1
)

type InfoHash [20]byte

type pieceSum [20]byte

func copyHashSum(dst, src []byte) {
	if len(dst) != len(src) || copy(dst, src) != len(dst) {
		panic("hash sum sizes differ")
	}
}

func BytesInfoHash(b []byte) (ih InfoHash) {
	if len(b) != len(ih) {
		panic("bad infohash bytes")
	}
	return
}

type pieceState uint8

const (
	pieceStateUnknown = iota
	pieceStateComplete
	pieceStateIncomplete
)

type piece struct {
	State pieceState
	Hash  pieceSum
}

type connection struct {
	Socket net.Conn
	post   chan peer_protocol.Message

	Interested bool
	Choked     bool
	Requests   []peer_protocol.Request

	PeerId         [20]byte
	PeerInterested bool
	PeerChoked     bool
	PeerRequests   []peer_protocol.Request
}

type torrent struct {
	InfoHash InfoHash
	Pieces   []piece
	Data     MMapSpan
	MetaInfo *metainfo.MetaInfo
	Conns    []connection
	Peers    []Peer
}

type Peer struct {
	Id   [20]byte
	IP   net.IP
	Port int
}

func (t torrent) PieceSize(piece int) (size int64) {
	if piece == len(t.Pieces)-1 {
		size = t.Data.Size() % t.MetaInfo.PieceLength
	}
	if size == 0 {
		size = t.MetaInfo.PieceLength
	}
	return
}

func (t torrent) PieceReader(piece int) io.Reader {
	return io.NewSectionReader(t.Data, int64(piece)*t.MetaInfo.PieceLength, t.MetaInfo.PieceLength)
}

func (t torrent) HashPiece(piece int) (ps pieceSum) {
	hash := PieceHash.New()
	n, err := io.Copy(hash, t.PieceReader(piece))
	if err != nil {
		panic(err)
	}
	if n != t.PieceSize(piece) {
		panic("hashed wrong number of bytes")
	}
	copyHashSum(ps[:], hash.Sum(nil))
	return
}

type client struct {
	DataDir       string
	HalfOpenLimit int
	PeerId        [20]byte

	halfOpen int
	torrents map[InfoHash]*torrent

	noTorrents      chan struct{}
	addTorrent      chan *torrent
	torrentFinished chan InfoHash
	actorTask       chan func()
}

func NewClient(dataDir string) *client {
	c := &client{
		DataDir:       dataDir,
		HalfOpenLimit: 10,

		torrents: make(map[InfoHash]*torrent),

		noTorrents:      make(chan struct{}),
		addTorrent:      make(chan *torrent),
		torrentFinished: make(chan InfoHash),
		actorTask:       make(chan func()),
	}
	_, err := rand.Read(c.PeerId[:])
	if err != nil {
		panic("error generating peer id")
	}
	go c.run()
	return c
}

func mmapTorrentData(metaInfo *metainfo.MetaInfo, location string) (mms MMapSpan, err error) {
	defer func() {
		if err != nil {
			mms.Close()
			mms = nil
		}
	}()
	for _, miFile := range metaInfo.Files {
		fileName := filepath.Join(append([]string{location, metaInfo.Name}, miFile.Path...)...)
		err = os.MkdirAll(filepath.Dir(fileName), 0666)
		if err != nil {
			return
		}
		var file *os.File
		file, err = os.OpenFile(fileName, os.O_CREATE|os.O_RDWR, 0666)
		if err != nil {
			return
		}
		var fi os.FileInfo
		fi, err = file.Stat()
		if err != nil {
			return
		}
		if fi.Size() < miFile.Length {
			err = file.Truncate(miFile.Length)
			if err != nil {
				return
			}
		}
		var mMap gommap.MMap
		mMap, err = gommap.MapRegion(file.Fd(), 0, miFile.Length, gommap.PROT_READ|gommap.PROT_WRITE, gommap.MAP_SHARED)
		if err != nil {
			return
		}
		if int64(len(mMap)) != miFile.Length {
			panic("mmap has wrong length")
		}
		mms = append(mms, MMap{mMap})
	}
	return
}

func (me *client) torrent(ih InfoHash) *torrent {
	for _, t := range me.torrents {
		if t.InfoHash == ih {
			return t
		}
	}
	return nil
}

func (me *client) initiateConn(peer Peer, torrent *torrent) {
	if peer.Id == me.PeerId {
		return
	}
	me.halfOpen++
	go func() {
		conn, err := net.DialTCP("tcp", nil, &net.TCPAddr{
			IP:   peer.IP,
			Port: peer.Port,
		})
		me.withContext(func() {
			me.halfOpen--
			me.openNewConns()
		})
		if err != nil {
			log.Printf("error connecting to peer: %s", err)
			return
		}
		me.runConnection(conn, torrent, peer.Id)
	}()
}

func (me *client) runConnection(conn net.Conn, torrent *torrent, peerId [20]byte) {
	log.Fatalf("connected to %s", conn)
}

func (me *client) openNewConns() {
	for _, t := range me.torrents {
		for len(t.Peers) != 0 {
			if me.halfOpen >= me.HalfOpenLimit {
				return
			}
			p := t.Peers[0]
			t.Peers = t.Peers[1:]
			me.initiateConn(p, t)
		}
	}
}

func (me *client) AddPeers(infoHash InfoHash, peers []Peer) (err error) {
	me.withContext(func() {
		t := me.torrent(infoHash)
		if t == nil {
			err = errors.New("no such torrent")
			return
		}
		t.Peers = append(t.Peers, peers...)
		me.openNewConns()
	})
	return
}

func (me *client) AddTorrent(metaInfo *metainfo.MetaInfo) error {
	torrent := &torrent{
		InfoHash: BytesInfoHash(metaInfo.InfoHash),
	}
	for offset := 0; offset < len(metaInfo.Pieces); offset += PieceHash.Size() {
		hash := metaInfo.Pieces[offset : offset+PieceHash.Size()]
		if len(hash) != PieceHash.Size() {
			return errors.New("bad piece hash in metainfo")
		}
		piece := piece{}
		copyHashSum(piece.Hash[:], hash)
		torrent.Pieces = append(torrent.Pieces, piece)
	}
	var err error
	torrent.Data, err = mmapTorrentData(metaInfo, me.DataDir)
	if err != nil {
		return err
	}
	torrent.MetaInfo = metaInfo
	me.addTorrent <- torrent
	return nil
}

func (me *client) WaitAll() {
	<-me.noTorrents
}

func (me *client) Close() {
}

func (me *client) withContext(f func()) {
	me.actorTask <- f
}

func (me *client) pieceHashed(ih InfoHash, piece int, correct bool) {
	torrent := me.torrents[ih]
	torrent.Pieces[piece].State = func() pieceState {
		if correct {
			return pieceStateComplete
		} else {
			return pieceStateIncomplete
		}
	}()
	for _, piece := range torrent.Pieces {
		if piece.State == pieceStateUnknown {
			return
		}
	}
	go func() {
		me.torrentFinished <- ih
	}()
}

func (me *client) run() {
	for {
		noTorrents := me.noTorrents
		if len(me.torrents) != 0 {
			noTorrents = nil
		}
		select {
		case noTorrents <- struct{}{}:
		case torrent := <-me.addTorrent:
			if _, ok := me.torrents[torrent.InfoHash]; ok {
				break
			}
			me.torrents[torrent.InfoHash] = torrent
			for i := range torrent.Pieces {
				go func(piece int) {
					sum := torrent.HashPiece(piece)
					me.withContext(func() {
						me.pieceHashed(torrent.InfoHash, piece, sum == torrent.Pieces[piece].Hash)
					})
				}(i)
			}
		case infoHash := <-me.torrentFinished:
			delete(me.torrents, infoHash)
		case task := <-me.actorTask:
			task()
		}
	}
}
