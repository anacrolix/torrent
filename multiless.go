package torrent

func strictCmp(same, less bool) cmper {
	return func() (bool, bool) { return same, less }
}

type (
	cmper     func() (same, less bool)
	multiLess struct {
		ok   bool
		less bool
	}
)

func (me *multiLess) Final() bool {
	if !me.ok {
		panic("undetermined")
	}
	return me.less
}

func (me *multiLess) FinalOk() (left, ok bool) {
	return me.less, me.ok
}

func (me *multiLess) Next(f cmper) {
	me.StrictNext(f())
}

func (me *multiLess) StrictNext(same, less bool) {
	if me.ok {
		return
	}
	if same {
		return
	}
	me.ok, me.less = true, less
}

func (me *multiLess) NextBool(l, r bool) {
	me.StrictNext(l == r, l)
}
