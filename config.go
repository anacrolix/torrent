package torrent

import (
	"crypto/tls"
	"net"
	"net/http"
	"time"

	"github.com/anacrolix/dht"
	"golang.org/x/time/rate"

	"github.com/anacrolix/torrent/iplist"
	"github.com/anacrolix/torrent/storage"
)

var DefaultHTTPClient = &http.Client{
	Timeout: time.Second * 15,
	Transport: &http.Transport{
		Dial: (&net.Dialer{
			Timeout: 15 * time.Second,
		}).Dial,
		TLSHandshakeTimeout: 15 * time.Second,
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: true},
	},
}

// Override Client defaults.
type Config struct {
	// Store torrent file data in this directory unless TorrentDataOpener is
	// specified.
	DataDir string `long:"data-dir" description:"directory to store downloaded torrent data"`
	// The address to listen for new uTP and TCP bittorrent protocol
	// connections. DHT shares a UDP socket with uTP unless configured
	// otherwise.
	ListenAddr string `long:"listen-addr" value-name:"HOST:PORT"`
	// Don't announce to trackers. This only leaves DHT to discover peers.
	DisableTrackers bool `long:"disable-trackers"`
	DisablePEX      bool `long:"disable-pex"`
	// Don't create a DHT.
	NoDHT bool `long:"disable-dht"`
	// Overrides the default DHT configuration.
	DHTConfig dht.ServerConfig

	// Never send chunks to peers.
	NoUpload bool `long:"no-upload"`
	// Disable uploading even when it isn't fair.
	DisableAggressiveUpload bool `long:"disable-aggressive-upload"`
	// Upload even after there's nothing in it for us. By default uploading is
	// not altruistic, we'll upload slightly more than we download from each
	// peer.
	Seed bool `long:"seed"`
	// Events are data bytes sent in pieces. The burst must be large enough to
	// fit a whole chunk, which is usually 16 KiB.
	UploadRateLimiter *rate.Limiter
	// The events are bytes read from connections. The burst must be bigger
	// than the largest Read performed on a Conn minus one. This is likely to
	// be the larger of the main read loop buffer (~4096), and the requested
	// chunk size (~16KiB).
	DownloadRateLimiter *rate.Limiter

	// User-provided Client peer ID. If not present, one is generated automatically.
	PeerID string
	// For the bittorrent protocol.
	DisableUTP bool
	// For the bittorrent protocol.
	DisableTCP bool `long:"disable-tcp"`
	// Called to instantiate storage for each added torrent. Builtin backends
	// are in the storage package. If not set, the "file" implementation is
	// used.
	DefaultStorage storage.ClientImpl

	EncryptionPolicy

	IPBlocklist iplist.Ranger
	DisableIPv6 bool `long:"disable-ipv6"`
	// Perform logging and any other behaviour that will help debug.
	Debug bool `help:"enable debugging"`

	// HTTP client used to query the tracker endpoint. Default is DefaultHTTPClient
	HTTP *http.Client
	// Updated occasionally to when there's been some changes to client
	// behaviour in case other clients are assuming anything of us. See also
	// `bep20`.
	ExtendedHandshakeClientVersion string // default  "go.torrent dev 20150624"
	// Peer ID client identifier prefix. We'll update this occasionally to
	// reflect changes to client behaviour that other clients may depend on.
	// Also see `extendedHandshakeClientVersion`.
	Bep20 string // default "-GT0001-"

	NominalDialTimeout         time.Duration // default  time.Second * 30
	MinDialTimeout             time.Duration // default  5 * time.Second
	EstablishedConnsPerTorrent int           // default 80
	HalfOpenConnsPerTorrent    int           // default  80
	TorrentPeersHighWater      int           // default 200
	TorrentPeersLowWater       int           // default 50

	// Limit how long handshake can take. This is to reduce the lingering
	// impact of a few bad apples. 4s loses 1% of successful handshakes that
	// are obtained with 60s timeout, and 5% of unsuccessful handshakes.
	HandshakesTimeout time.Duration // default  20 * time.Second
}

func (cfg *Config) setDefaults() {
	if cfg.HTTP == nil {
		cfg.HTTP = DefaultHTTPClient
	}
	if cfg.ExtendedHandshakeClientVersion == "" {
		cfg.ExtendedHandshakeClientVersion = "go.torrent dev 20150624"
	}
	if cfg.Bep20 == "" {
		cfg.Bep20 = "-GT0001-"
	}
	if cfg.NominalDialTimeout == 0 {
		cfg.NominalDialTimeout = 30 * time.Second
	}
	if cfg.MinDialTimeout == 0 {
		cfg.MinDialTimeout = 5 * time.Second
	}
	if cfg.EstablishedConnsPerTorrent == 0 {
		cfg.EstablishedConnsPerTorrent = 80
	}
	if cfg.HalfOpenConnsPerTorrent == 0 {
		cfg.HalfOpenConnsPerTorrent = 80
	}
	if cfg.TorrentPeersHighWater == 0 {
		cfg.TorrentPeersHighWater = 200
	}
	if cfg.TorrentPeersLowWater == 0 {
		cfg.TorrentPeersLowWater = 50
	}
	if cfg.HandshakesTimeout == 0 {
		cfg.HandshakesTimeout = 20 * time.Second
	}
}

type EncryptionPolicy struct {
	DisableEncryption  bool
	ForceEncryption    bool // Don't allow unobfuscated connections.
	PreferNoEncryption bool
}
