package main

import (
	"crypto/rand"
	"fmt"
	"github.com/anacrolix/envpprof"
	"github.com/anacrolix/log"
	"github.com/anacrolix/sync"
	"github.com/dustin/go-humanize"
	"golang.org/x/exp/slog"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

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
	cfg.DisablePEX = false
	cfg.NoDefaultPortForwarding = true
	cfg.Seed = true
	cfg.Debug = false
	return cfg
}

func main() {
	defer envpprof.Stop()

	// Create a Tmp directory: peers-bootstrappingXXXX
	tmpDir, err := os.MkdirTemp("", "peers-bootstrapping")
	assertNil(err)
	slog.Info("made temp dir", slog.String("tmpDir", tmpDir))

	// Create a directory called, peers-bootstrappingXXXX/source
	sourceDir := filepath.Join(tmpDir, "source")
	assertNil(os.Mkdir(sourceDir, 0o700))

	// Create a source file of 5GB size within peers-bootstrappingXXXX/source/file
	f, err := os.Create(filepath.Join(sourceDir, "file"))
	assertNil(err)
	// Create 5 GB binary file.
	_, err = io.CopyN(f, rand.Reader, int64(5)<<30)
	assertNil(err)
	assertNil(f.Close())

	// Create metaInfo from the source file.
	var info metainfo.Info
	err = info.BuildFromFilePath(f.Name())
	assertNil(err)
	var mi metainfo.MetaInfo
	mi.InfoBytes, err = bencode.Marshal(info)
	assertNil(err)

	// Containers for client[i] <-> torrents[i] <-> webSeeds to simulate the set_rackpeers() logic within the worker code.
	var clients []*torrent.Client
	var torrents []*torrent.Torrent
	var webSeeds []string

	clientIndex := 0
	addClientAndTorrentWebSeed := func(cl *torrent.Client, t *torrent.Torrent) int {
		clients = append(clients, cl)
		torrents = append(torrents, t)
		webSeeds = append(webSeeds, "file://"+sourceDir+"/file")
		ret := clientIndex
		http.HandleFunc(
			fmt.Sprintf("/%v", ret),
			func(w http.ResponseWriter, r *http.Request) {
				cl.WriteStatus(w)
			})
		clientIndex++
		return ret
	}

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

	for range 6 {
		storageDir := filepath.Join(tmpDir, fmt.Sprintf("client%v", clientIndex))
		clientIndex := clientIndex

		// Adding the special case for master i.e. node0/peer0/client0
		if clientIndex == 0 {
			sourceFile := filepath.Join(sourceDir, "file")
			destFile := filepath.Join(storageDir, "file")

			clientConfig := newClientConfig()
			// clientConfig.DefaultStorage = storage.NewMMap(sourceDir) <--- Why doeds this works?!

			// Why the below line doesn't work? In my codebase , the sourceDir and conf.DefaultStorage  is different.
			// when I uncomment the below line, Client0 seems to be stuck
			clientConfig.DefaultStorage = storage.NewMMap(storageDir) // <--- This does not work! Why ? !!

			clientConfig.Logger = log.Default.WithValues(slog.Int("clientIndex", clientIndex))
			client, err := torrent.NewClient(clientConfig)
			assertNil(err)

			// Using the same API that is used in our internal wrapper code.
			// After execution of the below "AddTorrent" API, the "client0/file gets created automatically!!"

			//---------------------------------------------------------------------------
			//[adasgupta@adasgupta-MBP16] peers-bootstrapping1372372411 % ls -lrth client0
			// total 0
			//-rw-r--r--  1 adasgupta  staff   5.0G Jun 28 00:41 file
			//---------------------------------------------------------------------------

			// t, _ := client.AddTorrent(&mi) //<--------- Should we use this? (I'm using this in my code)

			t, _ := client.AddTorrentInfoHash(mi.HashInfoBytes()) //<--- why this can't be used? When I use this, the torrent is stuck

			addClientAndTorrentWebSeed(client, t)
			t.AddWebSeeds([]string{webSeeds[clientIndex]})
			allDownloaded.Add(1)
			notCompleted.Store(clientIndex, nil)

			go func() {
				// Simulate the peer0/client0 copy operation i.e. From the source directory (where it downloads the binary)
				// to the destination i.e. config.DefaultStorage
				if err := os.Link(sourceFile, destFile); err != nil {
					log.Fatalf("failed to copy directory: %v", err)
				}
				<-t.GotInfo()
				t.DownloadAll()
				<-t.Complete().On()
				notCompleted.Delete(clientIndex)
				slog.Info("leecher completed", slog.Int("clientIndex", clientIndex))
				allDownloaded.Done()

			}()
		} else {
			// Rest of the nodes.
			clientConfig := newClientConfig()
			clientConfig.DefaultStorage = storage.NewMMap(storageDir)
			clientConfig.Logger = log.Default.WithValues(slog.Int("clientIndex", clientIndex))
			//clientConfig.Logger.Levelf(log.Critical, "test")
			client, err := torrent.NewClient(clientConfig)
			assertNil(err)

			t, _ := client.AddTorrentInfoHash(mi.HashInfoBytes())
			addClientAndTorrentWebSeed(client, t)

			t.AddWebSeeds([]string{webSeeds[clientIndex]})

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
			t.AddClientPeer(clients[0])
		}
	}

	go func() {
		for range time.Tick(time.Second) {
			// Nested for Loop that simulates the set_rackpeers() method which internally calls the AddPeers() API
			// after every HB response from master node.
			for _, t := range torrents {
				for _, cl := range clients {
					t.AddClientPeer(cl)
				}
			}

			// Simulates the stats that client recieves in the worker logs.
			for clientIndex, cl := range clients {
				metaInfo := torrents[clientIndex].Metainfo()
				t, _ := cl.Torrent(metaInfo.HashInfoBytes())
				if t != nil {
					pieceComplete := t.Stats().PiecesComplete
					totalPeers := t.Stats().TotalPeers
					totalPieces := t.NumPieces()
					pieceCompleteProgress := (float64(pieceComplete) / float64(totalPieces)) * 100
					fmt.Printf(
						"Client %v PiecesCompleted:  %v PieceProgress: %.2f%% TotalPieces: %v TotalPeers: %v\n",
						clientIndex,
						pieceComplete,
						pieceCompleteProgress,
						totalPieces,
						totalPeers,
					)
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
