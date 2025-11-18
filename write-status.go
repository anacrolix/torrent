package torrent

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"iter"
)

func indented(w io.Writer) iter.Seq[statusWriter] {
	return func(yield func(sw statusWriter) bool) {
		iw := newIndentWriter(w, "  ")
		sw := statusWriter{iw}
		yield(sw)
		sw.nl()
	}
}

type indentWriter struct {
	indent   string
	w        bufio.Writer
	indented bool
}

func newIndentWriter(w io.Writer, indent string) *indentWriter {
	ret := &indentWriter{
		indent: indent,
	}
	ret.w.Reset(w)
	return ret
}

func (me *indentWriter) Write(b []byte) (n int, err error) {
	for len(b) != 0 && err == nil {
		if !me.indented {
			me.w.WriteString(me.indent)
			me.indented = true
		}
		b1 := b
		i := bytes.IndexByte(b1, '\n')
		if i != -1 {
			b1 = b1[:i+1]
		}
		var n1 int
		n1, err = me.w.Write(b1)
		if n1 > 0 && n1 == i+1 {
			me.indented = false
		}
		n += n1
		b = b[n1:]
	}
	if err == nil {
		err = me.w.Flush()
	}
	return
}

type statusWriter struct {
	w io.Writer
}

func (me statusWriter) Write(b []byte) (n int, err error) {
	return me.w.Write(b)
}

func (me *statusWriter) f(fmtStr string, args ...any) {
	fmt.Fprintf(me, fmtStr, args...)
}

func (me *statusWriter) tab() *tableWriter {
	tw := &tableWriter{
		parent: me,
	}
	tw.statusWriter.w = &tw.buf
	return tw
}

func (me *statusWriter) nl() {
	fmt.Fprintln(me)
}

func (me *statusWriter) indented() iter.Seq[statusWriter] {
	return indented(me)
}

type tableWriter struct {
	parent *statusWriter
	cells  [][]string
	curRow []string
	buf    bytes.Buffer
	statusWriter
}

// Flushes/ends a column. Probably want to check we don't have buffered data before starting new
// table elements.
func (me *tableWriter) col() {
	me.curRow = append(me.curRow, me.buf.String())
	me.buf.Reset()
}

func (me *tableWriter) cols(args ...any) {
	for _, a := range args {
		fmt.Fprint(me, a)
		me.col()
	}
}

func (me *tableWriter) row() {
	me.col()
	me.cells = append(me.cells, me.curRow)
	me.curRow = nil
}

func (me *tableWriter) getColWidths() (widths []int) {
	// Imagine if we made this column-oriented...
	for _, row := range me.cells {
		for i, col := range row {
			for i >= len(widths) {
				widths = append(widths, 0)
			}
			widths[i] = max(widths[i], len(col))
		}
	}
	return
}

func (me *tableWriter) end() {
	widths := me.getColWidths()
	for _, row := range me.cells {
		// You could use an indentWriter here to maintain multi-line cells at the same indent.
		for i, col := range row {
			if i != 0 {
				me.parent.w.Write([]byte{' '})
			}
			fmt.Fprintf(me.parent, "%-*s", widths[i], col)
		}
		me.parent.nl()
	}
	me.parent = nil
}
