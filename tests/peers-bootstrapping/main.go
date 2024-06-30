package main

import (
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/anacrolix/envpprof"
	"github.com/anacrolix/log"
	"github.com/anacrolix/sync"
	"github.com/dustin/go-humanize"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

func assertNil(x any) {
	if x != nil {
		panic(x)
	}
}

func newClientConfig() *torrent.ClientConfig {
	cfg := torrent.NewDefaultClientConfig()
	cfg.ListenPort = 0
	cfg.NoDHT = true
	cfg.NoDefaultPortForwarding = true
	cfg.Seed = true
	cfg.Debug = false
	return cfg
}

func main() {
	defer envpprof.Stop()
	tmpDir, err := os.MkdirTemp("", "peers-bootstrapping")
	assertNil(err)
	slog.Info("made temp dir", slog.String("tmpDir", tmpDir))
	sourceDir := filepath.Join(tmpDir, "source")
	assertNil(os.Mkdir(sourceDir, 0o700))
	f, err := os.Create(filepath.Join(sourceDir, "file"))
	assertNil(err)
	_, err = io.CopyN(f, rand.Reader, 1<<30)
	assertNil(err)
	assertNil(f.Close())
	var info metainfo.Info
	err = info.BuildFromFilePath(f.Name())
	assertNil(err)
	var mi metainfo.MetaInfo
	mi.InfoBytes, err = bencode.Marshal(info)
	assertNil(err)
	var clients []*torrent.Client
	var torrents []*torrent.Torrent
	clientConfig := newClientConfig()
	clientConfig.DefaultStorage = storage.NewMMap(sourceDir)
	initialClient, err := torrent.NewClient(clientConfig)
	assertNil(err)
	clientIndex := 0
	addClientAndTorrent := func(cl *torrent.Client, t *torrent.Torrent) int {
		clients = append(clients, cl)
		torrents = append(torrents, t)
		ret := clientIndex
		http.HandleFunc(
			fmt.Sprintf("/%v", ret),
			func(w http.ResponseWriter, r *http.Request) {
				cl.WriteStatus(w)
			})
		clientIndex++
		return ret
	}
	initialTorrent, err := initialClient.AddTorrent(&mi)
	assertNil(err)
	addClientAndTorrent(initialClient, initialTorrent)
	//initialTorrent.VerifyData()
	<-initialTorrent.Complete().On()
	var allDownloaded sync.WaitGroup
	var notCompleted sync.Map
	http.HandleFunc(
		"/notCompleted",
		func(w http.ResponseWriter, r *http.Request) {
			notCompleted.Range(func(key, value any) bool {
				fmt.Fprintln(w, key)
				return true
			})
		})
	for range 5 {
		clientIndex := clientIndex
		storageDir := filepath.Join(tmpDir, fmt.Sprintf("client%v", clientIndex))
		clientConfig := newClientConfig()
		clientConfig.DefaultStorage = storage.NewMMap(storageDir)
		clientConfig.Logger = log.Default.WithValues(slog.Int("clientIndex", clientIndex))
		//clientConfig.Logger.Levelf(log.Critical, "test")
		client, err := torrent.NewClient(clientConfig)
		assertNil(err)
		t, _ := client.AddTorrentInfoHash(mi.HashInfoBytes())
		addClientAndTorrent(client, t)
		allDownloaded.Add(1)
		notCompleted.Store(clientIndex, nil)
		go func() {
			<-t.GotInfo()
			t.DownloadAll()
			<-t.Complete().On()
			notCompleted.Delete(clientIndex)
			slog.Info("leecher completed", slog.Int("clientIndex", clientIndex))
			allDownloaded.Done()
		}()
		t.AddClientPeer(initialClient)
	}
	go func() {
		for range time.Tick(time.Second) {
			for _, t := range torrents {
				for _, cl := range clients {
					t.AddClientPeer(cl)
				}
			}
		}
	}()
	allDownloaded.Wait()
	slog.Info("all leechers downloaded")
	for clientIndex, cl := range clients {
		stats := cl.Stats()
		written := stats.BytesWritten
		read := stats.BytesRead
		fmt.Printf(
			"client %v wrote %v read %v\n",
			clientIndex,
			humanize.Bytes(uint64(written.Int64())),
			humanize.Bytes(uint64(read.Int64())),
		)
	}
	assertNil(os.RemoveAll(tmpDir))
}
