package metainfo

import (
	"slices"
)

type AnnounceList [][]string

func (al AnnounceList) Clone() AnnounceList {
	return slices.Clone(al)
}

// Whether the AnnounceList should be preferred over a single URL announce.
func (al AnnounceList) OverridesAnnounce(announce string) bool {
	for _, tier := range al {
		for _, url := range tier {
			if url != "" || announce == "" {
				return true
			}
		}
	}
	return false
}

func (al AnnounceList) DistinctValues() (ret []string) {
	seen := make(map[string]struct{})
	for _, tier := range al {
		for _, v := range tier {
			if _, ok := seen[v]; !ok {
				seen[v] = struct{}{}
				ret = append(ret, v)
			}
		}
	}
	return
}
