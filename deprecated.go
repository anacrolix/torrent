package torrent

// Deprecated: The names doesn't reflect the actual behaviour. You can only add sources. Use
// AddSources instead.
func (t *Torrent) UseSources(sources []string) {
	t.AddSources(sources)
}
