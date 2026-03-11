//go:build !windows

package torrentfs

import (
	"strings"

	"github.com/anacrolix/torrent"
)

// DirEntry describes a single filesystem entry returned by a directory listing.
type DirEntry struct {
	Name  string
	IsDir bool
}

// TorrentLookup is the result of a successful lookup in a torrent filesystem.
type TorrentLookup struct {
	IsDir   bool
	Torrent *torrent.Torrent
	File    *torrent.File // nil when IsDir is true
	Path    string        // full path within the torrent
}

// RootEntries returns directory entries for the root of tfs.
func RootEntries(tfs *TorrentFS) []DirEntry {
	var entries []DirEntry
	for _, t := range tfs.Client.Torrents() {
		info := t.Info()
		if info == nil {
			continue
		}
		entries = append(entries, DirEntry{Name: info.BestName(), IsDir: info.IsDir()})
	}
	return entries
}

// RootLookup looks up name in the root of tfs.
func RootLookup(tfs *TorrentFS, name string) (TorrentLookup, bool) {
	for _, t := range tfs.Client.Torrents() {
		info := t.Info()
		if t.Name() != name || info == nil {
			continue
		}
		if !info.IsDir() {
			return TorrentLookup{Torrent: t, File: t.Files()[0], Path: name}, true
		}
		return TorrentLookup{IsDir: true, Torrent: t, Path: name}, true
	}
	return TorrentLookup{}, false
}

// dirDepth returns the number of path components in path. The root ("") has depth 0.
func dirDepth(path string) int {
	if path == "" {
		return 0
	}
	return len(strings.Split(path, "/"))
}

// DirEntries returns directory entries for a sub-directory at path within t.
func DirEntries(t *torrent.Torrent, path string) []DirEntry {
	info := t.Info()
	depth := dirDepth(path)
	names := map[string]bool{}
	var entries []DirEntry
	for _, fi := range info.UpvertedFiles() {
		filePathname := strings.Join(fi.BestPath(), "/")
		if !IsSubPath(path, filePathname) {
			continue
		}
		name := fi.BestPath()[depth]
		if names[name] {
			continue
		}
		names[name] = true
		entries = append(entries, DirEntry{
			Name:  name,
			IsDir: len(fi.BestPath()) != depth+1,
		})
	}
	return entries
}

// DirLookup looks up name in the sub-directory at path within torrent t.
func DirLookup(t *torrent.Torrent, path, name string) (TorrentLookup, bool) {
	var fullPath string
	if path != "" {
		fullPath = path + "/" + name
	} else {
		fullPath = name
	}
	dir := false
	var file *torrent.File
	for _, f := range t.Files() {
		if f.DisplayPath() == fullPath {
			file = f
		}
		if IsSubPath(fullPath, f.DisplayPath()) {
			dir = true
		}
	}
	if dir && file != nil {
		panic("both dir and file")
	}
	if file != nil {
		return TorrentLookup{Torrent: t, File: file, Path: fullPath}, true
	}
	if dir {
		return TorrentLookup{IsDir: true, Torrent: t, Path: fullPath}, true
	}
	return TorrentLookup{}, false
}
