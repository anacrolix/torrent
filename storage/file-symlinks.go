package storage

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Checks that the parts of a torrent's file paths that already exist stay inside dir, and are
// directories or regular files. Symlinks are followed only if they resolve inside dir. This is
// checked when the torrent is opened, so it doesn't stop links being created later.
type torrentDirLinkChecker struct {
	dir     string
	realDir string
	// Paths under dir that have been checked, and whether they exist.
	checked map[string]bool
}

func newTorrentDirLinkChecker(dir string) (_ *torrentDirLinkChecker, err error) {
	ret := &torrentDirLinkChecker{
		dir:     dir,
		checked: make(map[string]bool),
	}
	ret.realDir, err = filepath.EvalSymlinks(dir)
	if errors.Is(err, fs.ErrNotExist) {
		// Nothing can exist under dir yet.
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("resolving torrent dir: %w", err)
	}
	return ret, nil
}

// Checks the path, which must be under the checker's dir. A nil checker accepts everything.
func (me *torrentDirLinkChecker) check(filePath string) error {
	if me == nil {
		return nil
	}
	rel, err := filepath.Rel(me.dir, filePath)
	if err != nil {
		return err
	}
	comps := strings.Split(rel, string(filepath.Separator))
	cur := me.dir
	for i, comp := range comps {
		cur = filepath.Join(cur, comp)
		last := i == len(comps)-1
		exists, ok := me.checked[cur]
		if !ok {
			exists, err = me.checkComponent(cur, last)
			if err != nil {
				return err
			}
			me.checked[cur] = exists
		}
		if !exists {
			return nil
		}
	}
	return nil
}

// Checks one existing path component, which must be a regular file if last, or a directory
// otherwise. Returns false if it doesn't exist.
func (me *torrentDirLinkChecker) checkComponent(p string, last bool) (exists bool, err error) {
	fi, err := os.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		var resolved string
		resolved, err = filepath.EvalSymlinks(p)
		if err != nil {
			return false, fmt.Errorf("resolving symlink %q: %w", p, err)
		}
		if !isSubFilepath(me.realDir, resolved) {
			return false, fmt.Errorf("symlink %q resolves outside torrent dir %q", p, me.dir)
		}
		fi, err = os.Stat(p)
		if err != nil {
			return
		}
	}
	if last {
		if !fi.Mode().IsRegular() {
			return false, fmt.Errorf("%q exists and is not a regular file", p)
		}
	} else if !fi.IsDir() {
		return false, fmt.Errorf("%q exists and is not a directory", p)
	}
	return true, nil
}

// Applies torrentDirLinkChecker to each file path, and its part file path if part files are used.
func checkTorrentFileLinks(dir string, filePaths []string, partFiles bool) error {
	checker, err := newTorrentDirLinkChecker(dir)
	if err != nil {
		return err
	}
	for i, filePath := range filePaths {
		err = checker.check(filePath)
		if err == nil && partFiles {
			err = checker.check(filePath + partFileSuffix)
		}
		if err != nil {
			return fmt.Errorf("file %v: %w", i, err)
		}
	}
	return nil
}
