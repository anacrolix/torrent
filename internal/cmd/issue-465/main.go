package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
)

func main() {
	if err := dlTorrents("."); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}

func dlTorrents(dir string) error {
	conf := torrent.NewDefaultClientConfig()
	conf.DataDir = dir
	cl, err := torrent.NewClient(conf)
	if err != nil {
		return err
	}
	ids := []string{
		"urlteam_2021-02-03-21-17-02",
		"urlteam_2021-02-02-11-17-02",
		"urlteam_2021-01-31-11-17-02",
		"urlteam_2021-01-30-21-17-01",
		"urlteam_2021-01-29-21-17-01",
		"urlteam_2021-01-28-11-17-01",
		"urlteam_2021-01-27-11-17-02",
		"urlteam_2021-01-26-11-17-02",
		"urlteam_2021-01-25-03-17-02",
		"urlteam_2021-01-24-03-17-02",
	}
	for _, id := range ids {
		t, err := addTorrentFromURL(cl, fmt.Sprintf("https://archive.org/download/%s/%s_archive.torrent", id, id))
		if err != nil {
			return err
		}
		t.DownloadAll()
	}
	if !cl.WaitAll() {
		return errors.New("client stopped early")
	}
	return nil
}

func addTorrentFromURL(cl *torrent.Client, url string) (*torrent.Torrent, error) {
	fmt.Printf("Adding torrent: %s\n", url)
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %s", resp.Status)
	}
	defer resp.Body.Close()
	meta, err := metainfo.Load(resp.Body)
	if err != nil {
		return nil, err
	}
	return cl.AddTorrent(meta)
}
