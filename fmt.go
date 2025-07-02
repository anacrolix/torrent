package torrent

import (
	"fmt"
	"strings"
)

func formatMap[K comparable, V any](m map[K]V) string {
	var sb strings.Builder
	for k, v := range m {
		fmt.Fprintf(&sb, "%v: %v\n", k, v)
	}
	return strings.TrimSuffix(sb.String(), "\n")
}
