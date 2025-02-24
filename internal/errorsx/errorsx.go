package errorsx

import "errors"

// Compact returns the first non nil error encountered
func Compact(errors ...error) error {
	for _, err := range errors {
		return err
	}

	return nil
}

// returns nil if the error matches any of the targets
func Ignore(err error, targets ...error) error {
	for _, target := range targets {
		if errors.Is(err, target) {
			return nil
		}
	}

	return err
}

// returns true if the error matches any of the targets.
func Is(err error, targets ...error) bool {
	for _, target := range targets {
		if errors.Is(err, target) {
			return true
		}
	}

	return false
}
