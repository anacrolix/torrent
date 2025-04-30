//go:build !android

package possumTorrentStorage

import (
	"fmt"
	"io"
	"sort"
	"strconv"

	"github.com/anacrolix/log"
	possum "github.com/anacrolix/possum/go"
	possumResource "github.com/anacrolix/possum/go/resource"

	"github.com/anacrolix/torrent/storage"
)

// Extends possum resource.Provider with an efficient implementation of torrent
// storage.ConsecutiveChunkReader. TODO: This doesn't expose Capacity. TODO: Add a MarkComplete
// method that renames incomplete chunks rather than writing them to a single giant key and deleting
// them.
type Provider struct {
	possumResource.Provider
	Logger log.Logger
}

var _ storage.ConsecutiveChunkReader = Provider{}

// TODO: Should the parent ReadConsecutiveChunks method take the expected number of bytes to avoid
// trying to read discontinuous or incomplete sequences of chunks?
func (p Provider) ReadConsecutiveChunks(prefix string) (rc io.ReadCloser, err error) {
	p.Logger.Levelf(log.Debug, "ReadConsecutiveChunks(%q)", prefix)
	//debug.PrintStack()
	pr, err := p.Handle.NewReader()
	if err != nil {
		return
	}
	defer func() {
		if err != nil {
			pr.End()
		}
	}()
	items, err := pr.ListItems(prefix)
	if err != nil {
		return
	}
	keys := make([]int64, 0, len(items))
	for _, item := range items {
		var i int64
		offsetStr := item.Key
		i, err = strconv.ParseInt(offsetStr, 10, 64)
		if err != nil {
			err = fmt.Errorf("failed to parse offset %q: %w", offsetStr, err)
			return
		}
		keys = append(keys, i)
	}
	sort.Sort(keySorter[possum.Item, int64]{items, keys})
	offset := int64(0)
	consValues := make([]consecutiveValue, 0, len(items))
	for i, item := range items {
		itemOffset := keys[i]
		if itemOffset > offset {
			// We can't provide a continuous read.
			break
		}
		if itemOffset+item.Stat.Size() <= offset {
			// This item isn't needed
			continue
		}
		var v possum.Value
		v, err = pr.Add(prefix + item.Key)
		if err != nil {
			return
		}
		consValues = append(consValues, consecutiveValue{
			pv:     v,
			offset: itemOffset,
			size:   item.Stat.Size(),
		})
		offset += item.Stat.Size() - (offset - itemOffset)
	}
	err = pr.Begin()
	if err != nil {
		return
	}
	rc, pw := io.Pipe()
	go func() {
		defer pr.End()
		err := p.writeConsecutiveValues(consValues, pw)
		err = pw.CloseWithError(err)
		if err != nil {
			panic(err)
		}
	}()
	return
}

type consecutiveValue struct {
	pv     possum.Value
	offset int64
	size   int64
}

func (pp Provider) writeConsecutiveValues(
	values []consecutiveValue, pw *io.PipeWriter,
) (err error) {
	off := int64(0)
	for _, v := range values {
		var n int64
		valueOff := off - v.offset
		n, err = io.Copy(pw, io.NewSectionReader(v.pv, valueOff, v.size-valueOff))
		if err != nil {
			return
		}
		off += n
	}
	return nil
}

func (pp Provider) MovePrefix(from, to string) (err error) {
	return pp.Handle.MovePrefix([]byte(from), []byte(to))
}
