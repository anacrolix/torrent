package torrent

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// Session persistence of lightweight torrent state across client restarts.
// We persist a small set of data:
// - Reserve peers (addresses) to bootstrap connections without waiting for DHT/trackers.
// - Last tracker announce completion time and interval per tracker URL to honor backoffs.

type persistedTrackerState struct {
	URL             string `json:"url"`
	IntervalSeconds int64  `json:"interval_seconds"`
	CompletedUnix   int64  `json:"completed_unix"`
}

type torrentSessionState struct {
	// Peers are serialized as host:port strings.
	Peers []string `json:"peers,omitempty"`
	// Tracker states are keyed by full tracker URL string.
	Trackers map[string]persistedTrackerState `json:"trackers,omitempty"`
}

var sessionFileMu sync.Mutex

func (t *Torrent) sessionDirPath() (string, bool) {
	dataDir := t.cl.config.DataDir
	if dataDir == "" {
		return "", false
	}
	return filepath.Join(dataDir, ".session"), true
}

func (t *Torrent) sessionFilePath() (string, bool) {
	sessionDir, ok := t.sessionDirPath()
	if !ok {
		return "", false
	}
	return filepath.Join(sessionDir, t.InfoHash().HexString()+".json"), true
}

func readSessionFile(path string) (st torrentSessionState, _ error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return torrentSessionState{}, nil
	}
	if err != nil {
		return torrentSessionState{}, err
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	err = dec.Decode(&st)
	if err != nil {
		return torrentSessionState{}, err
	}
	if st.Trackers == nil {
		st.Trackers = make(map[string]persistedTrackerState)
	}
	return st, nil
}

func writeSessionFile(path string, st torrentSessionState) error {
	tmp := path + ".tmp"
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(&st); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// persistTrackerState upserts the saved announce state for the given tracker URL.
func (t *Torrent) persistTrackerState(trackerURL string, ar trackerAnnounceResult) {
	path, ok := t.sessionFilePath()
	if !ok {
		return
	}
	sessionFileMu.Lock()
	defer sessionFileMu.Unlock()
	st, _ := readSessionFile(path)
	if st.Trackers == nil {
		st.Trackers = make(map[string]persistedTrackerState)
	}
	// Clamp to seconds precision to avoid JSON bloat and simplify comparisons.
	ps := persistedTrackerState{
		URL:             trackerURL,
		IntervalSeconds: int64(ar.Interval / time.Second),
		CompletedUnix:   ar.Completed.Unix(),
	}
	st.Trackers[trackerURL] = ps
	_ = writeSessionFile(path, st)
}

// loadPersistedTrackerAnnounce returns a saved announce state for the tracker URL if present.
func (t *Torrent) loadPersistedTrackerAnnounce(trackerURL string) (trackerAnnounceResult, bool) {
	path, ok := t.sessionFilePath()
	if !ok {
		return trackerAnnounceResult{}, false
	}
	sessionFileMu.Lock()
	defer sessionFileMu.Unlock()
	st, err := readSessionFile(path)
	if err != nil {
		return trackerAnnounceResult{}, false
	}
	ps, ok := st.Trackers[trackerURL]
	if !ok {
		return trackerAnnounceResult{}, false
	}
	return trackerAnnounceResult{
		Err:       nil,
		NumPeers:  0,
		Interval:  time.Duration(ps.IntervalSeconds) * time.Second,
		Completed: time.Unix(ps.CompletedUnix, 0),
	}, true
}

// persistPeers writes a compact set of reserve peers for bootstrap. We cap count to avoid
// unbounded files and keep the most recently added peers (arbitrary order is acceptable).
func (t *Torrent) persistPeers() {
	path, ok := t.sessionFilePath()
	if !ok {
		return
	}
	// Snapshot peers. This is called from Torrent close callbacks while the Client lock is held,
	// so we must not attempt to acquire any Client locks here.
	var addrs []string
	// Don't iterate massive lists.
	const maxPeersToPersist = 200
	count := 0
	t.peers.Each(func(pi PeerInfo) {
		if count >= maxPeersToPersist {
			return
		}
		addrs = append(addrs, pi.Addr.String())
		count++
	})

	sessionFileMu.Lock()
	defer sessionFileMu.Unlock()
	st, _ := readSessionFile(path)
	st.Peers = addrs
	_ = writeSessionFile(path, st)
}

// loadPersistedPeers reads peers from disk and converts to PeerInfo slice.
func (t *Torrent) loadPersistedPeers() (ret []PeerInfo) {
	path, ok := t.sessionFilePath()
	if !ok {
		return nil
	}
	sessionFileMu.Lock()
	defer sessionFileMu.Unlock()
	st, err := readSessionFile(path)
	if err != nil {
		return nil
	}
	for _, s := range st.Peers {
		// Parse "host:port"
		ipPort, ok := parseIpPortAddrString(s)
		if !ok {
			continue
		}
		ret = append(ret, PeerInfo{
			Addr:    ipPort,
			Source:  PeerSourceIncoming, // nominal
			Trusted: false,
		})
	}
	return
}

func parseIpPortAddrString(s string) (ipPortAddr, bool) {
	host, portStr, err := net.SplitHostPort(s)
	if err != nil {
		return ipPortAddr{}, false
	}
	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		return ipPortAddr{}, false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return ipPortAddr{}, false
	}
	return ipPortAddr{IP: ip, Port: int(port)}, true
}

// saveSessionState persists both peers and tracker states opportunistically.
func (t *Torrent) saveSessionState() {
	t.persistPeers()
	// Trackers are persisted incrementally on each announce; no-op here.
}
