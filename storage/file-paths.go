package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/anacrolix/torrent/metainfo"
)

// Determines the filepath to be used for each file in a torrent.
type FilePathMaker func(opts FilePathMakerOpts) string

// Determines the directory for a given torrent within a storage client.
type TorrentDirFilePathMaker func(baseDir string, info *metainfo.Info, infoHash metainfo.Hash) string

// Info passed to a FilePathMaker.
type FilePathMakerOpts struct {
	Info *metainfo.Info
	File *metainfo.FileInfo
}

// defaultPathMaker just returns the storage client's base directory.
func defaultPathMaker(baseDir string, info *metainfo.Info, infoHash metainfo.Hash) string {
	return baseDir
}

func infoHashPathMaker(baseDir string, info *metainfo.Info, infoHash metainfo.Hash) string {
	return filepath.Join(baseDir, infoHash.HexString())
}

func isSubFilepath(base, sub string) bool {
	rel, err := filepath.Rel(base, sub)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

// Checks that the OS paths for a torrent's files are distinct files under dir. Files can't share a
// path, be at the torrent directory itself, be at another file's part file path (when part files
// are used), or be at a path another file needs as a directory. Any of those would have files
// truncate or overwrite each other's data. Padding files (BEP 47) of the same length may share a
// path, since they all hold the same zeroes: some torrent creators name them by their length.
func checkTorrentFilePaths(dir string, filePaths []string, fileInfos []metainfo.FileInfo, partFiles bool) error {
	type claim struct {
		fileIndex int
		isDir     bool
	}
	claims := make(map[string]claim)
	add := func(rel string, fileIndex int, isDir bool) error {
		prev, ok := claims[rel]
		if !ok {
			claims[rel] = claim{fileIndex, isDir}
			return nil
		}
		if isDir && prev.isDir {
			return nil
		}
		if !isDir && !prev.isDir && samePaddingFile(&fileInfos[fileIndex], &fileInfos[prev.fileIndex]) {
			return nil
		}
		return fmt.Errorf("file %v: path %q conflicts with file %v", fileIndex, rel, prev.fileIndex)
	}
	for i, filePath := range filePaths {
		rel, err := filepath.Rel(dir, filePath)
		if err != nil {
			return fmt.Errorf("file %v: %w", i, err)
		}
		if rel == "." {
			return fmt.Errorf("file %v: path %q is the torrent directory", i, filePath)
		}
		if err := add(rel, i, false); err != nil {
			return err
		}
		if partFiles {
			if err := add(rel+partFileSuffix, i, false); err != nil {
				return err
			}
		}
		for d := filepath.Dir(rel); d != "."; d = filepath.Dir(d) {
			if err := add(d, i, true); err != nil {
				return err
			}
		}
	}
	return nil
}

// Whether the file is a padding file (BEP 47).
func isPaddingFile(fi *metainfo.FileInfo) bool {
	return strings.ContainsRune(fi.Attr, 'p')
}

// Whether two files are padding files with the same contents, so they can share a path.
func samePaddingFile(a, b *metainfo.FileInfo) bool {
	return isPaddingFile(a) && isPaddingFile(b) && a.Length == b.Length
}
