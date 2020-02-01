package stringsx

import "strings"

// Default returns the first not blank string.
// if all are blank then it returns an empty string.
func Default(candidates ...string) string {
	for _, s := range candidates {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}

	return ""
}
