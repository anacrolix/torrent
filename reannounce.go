package torrent

// Reannounce asks each of the torrent's regular (HTTP and UDP) trackers for peers now, whatever
// interval the tracker asked for, with a regular announce. A tracker whose last announce failed
// keeps its retry delay. Websocket trackers are not affected. It does nothing for a dropped
// torrent.
func (t *Torrent) Reannounce() {
	t.cl.lock()
	defer t.cl.unlock()
	if t.isDropped() {
		return
	}
	d := &t.cl.regularTrackerAnnounceDispatcher
	for key := range t.regularTrackerAnnounceState {
		if state := d.announceStates[key]; state != nil {
			state.reannounce = true
			d.syncAnnounceState(key)
		}
	}
	d.updateTimer()
}
