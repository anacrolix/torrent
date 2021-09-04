package metainfo

type AnnounceList [][]string

func (al AnnounceList) Clone() (dup AnnounceList) {
	dup = make([][]string, len(al))
	for i := 0; i < len(al); i += 1 {
		dup[i] = make([]string, len(al[i]))
		copy(dup[i], al[i])
	}
	return
}

// Whether the AnnounceList should be preferred over a single URL announce.
func (al AnnounceList) OverridesAnnounce(announce string) (b bool) {
	if announce == "" {
		return true
	}
	for tier := 0; tier < len(al); tier++ {
		url, i := al[tier], 0
		for i < len(url) {
			if url[i] != "" {
				return true
			}
			i++
		}
	}
	return
}

func (al AnnounceList) DistinctValues() []string {
	seen := make(map[string]struct{})
	for tier := range al {
		for _, v := range al[tier] {
			if _, ok := seen[v]; !ok {
				seen[v] = struct{}{}
			}
		}
	}
	i := 0
	ret := make([]string, len(seen))
	for k := range seen {
		ret[i] = k
		i++
	}
	return ret
}
