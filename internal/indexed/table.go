package indexed

// Table-only stuff. table is the internal common-relation stuff.
type Table[R comparable] struct {
	table[R]
}

func (me *Table[R]) AddInsteadOf(f InsteadOf[R]) {
	me.insteadOf = append(me.insteadOf, f)
}
