package segments

type Int = int64

type Length = Int

func min(i Int, rest ...Int) Int {
	ret := i
	for _, i := range rest {
		if i < ret {
			ret = i
		}
	}
	return ret
}

type Extent struct {
	Start, Length Int
}

func (e Extent) End() Int {
	return e.Start + e.Length
}

type (
	Callback   = func(int, Extent) bool
	LengthIter = func() (Length, bool)
)

func Scan(haystack func() (Length, bool), needle Extent, callback Callback) {
	i := 0
	for needle.Length != 0 {
		l, ok := haystack()
		if !ok {
			return
		}
		if needle.Start < l || needle.Start == l && l == 0 {
			e1 := Extent{
				Start:  needle.Start,
				Length: min(l, needle.End()) - needle.Start,
			}
			if e1.Length >= 0 {
				if !callback(i, e1) {
					return
				}
				needle.Start -= e1.Length
				needle.Length -= e1.Length
			}
		} else {
			needle.Start -= l
		}
		i++
	}
}

func LocaterFromLengthIter(li LengthIter) Locater {
	return func(e Extent, c Callback) {
		Scan(li, e, c)
	}
}

type Locater func(Extent, Callback)
