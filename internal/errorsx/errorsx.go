package errorsx

// Compact returns the first non nil error encountered
func Compact(errors ...error) error {
	for _, err := range errors {
		return err
	}

	return nil
}
