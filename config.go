package torrent

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/anacrolix/dht/v2"
	"github.com/anacrolix/dht/v2/krpc"
	"github.com/anacrolix/log"
	"github.com/anacrolix/missinggo/v2"

	"github.com/pion/webrtc/v4"
	"golang.org/x/time/rate"

	"github.com/anacrolix/torrent/iplist"
	"github.com/anacrolix/torrent/metainfo"

	"github.com/anacrolix/torrent/mse"
	"github.com/anacrolix/torrent/storage"
	"github.com/anacrolix/torrent/version"
)

// Contains config elements that are exclusive to tracker handling. There may be other fields in
// ClientConfig that are also relevant.
type ClientTrackerConfig struct {
	// Don't announce to trackers. This only leaves DHT to discover peers.
	DisableTrackers bool `long:"disable-trackers"`
	// Defines DialContext func to use for HTTP tracker announcements
	TrackerDialContext func(ctx context.Context, network, addr string) (net.Conn, error)
	// Defines ListenPacket func to use for UDP tracker announcements
	TrackerListenPacket func(network, addr string) (net.PacketConn, error)
	// Takes a tracker's hostname and requests DNS A and AAAA records.
	// Used in case DNS lookups require a special setup (i.e., dns-over-https)
	LookupTrackerIp func(*url.URL) ([]net.IP, error)
}

type ClientDhtConfig struct {
	// Don't create a DHT.
	NoDHT            bool `long:"disable-dht"`
	DhtStartingNodes func(network string) dht.StartingNodesGetter
	// Called for each anacrolix/dht Server created for the Client.
	ConfigureAnacrolixDhtServer       func(*dht.ServerConfig)
	PeriodicallyAnnounceTorrentsToDht bool
	// OnQuery hook func
	DHTOnQuery func(query *krpc.Msg, source net.Addr) (propagate bool)
}

// Probably not safe to modify this after it's given to a Client, or to pass it to multiple Clients.
type ClientConfig struct {
	ClientTrackerConfig
	ClientDhtConfig
	MetainfoSourcesConfig

	// Store torrent file data in this directory unless DefaultStorage is
	// specified.
	DataDir string `long:"data-dir" description:"directory to store downloaded torrent data"`
	// The address to listen for new uTP and TCP BitTorrent protocol connections. DHT shares a UDP
	// socket with uTP unless configured otherwise.
	ListenHost              func(network string) string
	ListenPort              int
	NoDefaultPortForwarding bool
	UpnpID                  string
	DisablePEX              bool `long:"disable-pex"`

	// Never send chunks to peers.
	NoUpload bool `long:"no-upload"`
	// Disable uploading even when it isn't fair.
	DisableAggressiveUpload bool `long:"disable-aggressive-upload"`
	// Upload even after there's nothing in it for us. By default uploading is
	// not altruistic, we'll only upload to encourage the peer to reciprocate.
	Seed bool `long:"seed"`
	// Only applies to chunks uploaded to peers, to maintain responsiveness communicating local
	// Client state to peers. Each limiter token represents one byte. The Limiter's burst must be
	// large enough to fit a whole chunk, which is usually 16 KiB (see TorrentSpec.ChunkSize). If
	// limit is not Inf, and burst is left at 0, the implementation will choose a suitable burst.
	UploadRateLimiter *rate.Limiter
	// Rate limits all reads from connections to peers. Each limiter token represents one byte. The
	// Limiter's burst must be bigger than the largest Read performed on the underlying
	// rate-limiting io.Reader minus one. This is likely to be the larger of the main read loop
	// buffer (~4096), and the requested chunk size (~16KiB, see TorrentSpec.ChunkSize). If limit is
	// not Inf, and burst is left at 0, the implementation will choose a suitable burst.
	//
	// If the field is nil, no rate limiting is applied. And it can't be adjusted dynamically.
	DownloadRateLimiter *rate.Limiter
	// Maximum unverified bytes across all torrents. Not used if zero.
	MaxUnverifiedBytes int64

	// User-provided Client peer ID. If not present, one is generated automatically.
	PeerID string
	// For the bittorrent protocol.
	DisableUTP bool
	// For the bittorrent protocol.
	DisableTCP bool `long:"disable-tcp"`
	// Called to instantiate storage for each added torrent. Builtin backends
	// are in the storage package. If not set, the "file" implementation is
	// used (and Closed when the Client is Closed).
	DefaultStorage storage.ClientImpl

	HeaderObfuscationPolicy HeaderObfuscationPolicy
	// The crypto methods to offer when initiating connections with header obfuscation.
	CryptoProvides mse.CryptoMethod
	// Chooses the crypto method to use when receiving connections with header obfuscation.
	CryptoSelector mse.CryptoSelector

	IPBlocklist      iplist.Ranger
	DisableIPv6      bool `long:"disable-ipv6"`
	DisableIPv4      bool
	DisableIPv4Peers bool
	// Perform logging and any other behaviour that will help debug.
	Debug   bool `help:"enable debugging"`
	Logger  log.Logger
	Slogger *slog.Logger

	// Used for torrent sources and webseeding if set.
	WebTransport http.RoundTripper
	// Defines proxy for HTTP requests, such as for trackers. It's commonly set from the result of
	// "net/http".ProxyURL(HTTPProxy).
	HTTPProxy func(*http.Request) (*url.URL, error)
	// Defines DialContext func to use for HTTP requests, such as for fetching metainfo and webtorrent seeds
	HTTPDialContext func(ctx context.Context, network, addr string) (net.Conn, error)
	// HTTPUserAgent changes default UserAgent for HTTP requests
	HTTPUserAgent string
	// HttpRequestDirector modifies the request before it's sent.
	// Useful for adding authentication headers, for example
	HttpRequestDirector func(*http.Request) error
	// WebsocketTrackerHttpHeader returns a custom header to be used when dialing a websocket connection
	// to the tracker. Useful for adding authentication headers
	WebsocketTrackerHttpHeader func() http.Header
	// Updated occasionally to when there's been some changes to client
	// behaviour in case other clients are assuming anything of us. See also
	// `bep20`.
	ExtendedHandshakeClientVersion string
	// Peer ID client identifier prefix. We'll update this occasionally to
	// reflect changes to client behaviour that other clients may depend on.
	// Also see `extendedHandshakeClientVersion`.
	Bep20 string

	// Peer dial timeout to use when there are limited peers.
	NominalDialTimeout time.Duration
	// Minimum peer dial timeout to use (even if we have lots of peers).
	MinDialTimeout             time.Duration
	EstablishedConnsPerTorrent int
	HalfOpenConnsPerTorrent    int
	TotalHalfOpenConns         int
	// Maximum number of peer addresses in reserve.
	TorrentPeersHighWater int
	// Minumum number of peers before effort is made to obtain more peers.
	TorrentPeersLowWater int

	// Limit how long handshake can take. This is to reduce the lingering
	// impact of a few bad apples. 4s loses 1% of successful handshakes that
	// are obtained with 60s timeout, and 5% of unsuccessful handshakes.
	HandshakesTimeout time.Duration
	// How long between writes before sending a keep alive message on a peer connection that we want
	// to maintain.
	KeepAliveTimeout time.Duration
	// Maximum bytes to buffer per peer connection for peer request data before it is sent. This
	// must be >= the request chunk size from peers.
	MaxAllocPeerRequestDataPerConn int

	// The IP addresses as our peers should see them. May differ from the
	// local interfaces due to NAT or other network configurations.
	PublicIp4 net.IP
	PublicIp6 net.IP

	// Accept rate limiting affects excessive connection attempts from IPs that fail during
	// handshakes or request torrents that we don't have.
	DisableAcceptRateLimiting bool
	// Don't add connections that have the same peer ID as an existing
	// connection for a given Torrent.
	DropDuplicatePeerIds bool
	// Drop peers that are complete if we are also complete and have no use for the peer. This is a
	// bit of a special case, since a peer could also be useless if they're just not interested, or
	// we don't intend to obtain all of a torrent's data.
	DropMutuallyCompletePeers bool
	// Use dialers to obtain connections to regular peers.
	DialForPeerConns bool
	// Whether to accept peer connections at all.
	AcceptPeerConnections bool
	// Whether a Client should want conns without delegating to any attached Torrents. This is
	// useful when torrents might be added dynamically in callbacks for example.
	AlwaysWantConns bool

	Extensions PeerExtensionBits
	// Bits that peers must have set to proceed past handshakes.
	MinPeerExtensions PeerExtensionBits

	DisableWebtorrent bool
	DisableWebseeds   bool

	Callbacks Callbacks

	// ICEServerList defines a slice describing servers available to be used by
	// ICE, such as STUN and TURN servers.
	ICEServerList []webrtc.ICEServer

	// Deprecated. ICEServers does not support server authentication and therefore
	// it cannot be used with most TURN servers. Use ICEServerList instead.
	// ICEServers is kept for legacy support.
	ICEServers []string

	DialRateLimiter *rate.Limiter

	PieceHashersPerTorrent int // default: 2
}

func (cfg *ClientConfig) SetListenAddr(addr string) *ClientConfig {
	host, port, err := missinggo.ParseHostPort(addr)
	if err != nil {
		panic(err)
	}
	cfg.ListenHost = func(string) string { return host }
	cfg.ListenPort = port
	return cfg
}

func NewDefaultClientConfig() *ClientConfig {
	cc := &ClientConfig{
		HTTPUserAgent:                  version.DefaultHttpUserAgent,
		ExtendedHandshakeClientVersion: version.DefaultExtendedHandshakeClientVersion,
		Bep20:                          version.DefaultBep20Prefix,
		UpnpID:                         version.DefaultUpnpId,
		NominalDialTimeout:             20 * time.Second,
		MinDialTimeout:                 3 * time.Second,
		EstablishedConnsPerTorrent:     50,
		HalfOpenConnsPerTorrent:        25,
		TotalHalfOpenConns:             100,
		TorrentPeersHighWater:          500,
		TorrentPeersLowWater:           50,
		HandshakesTimeout:              4 * time.Second,
		KeepAliveTimeout:               time.Minute,
		MaxAllocPeerRequestDataPerConn: 1 << 20,
		ListenHost:                     func(string) string { return "" },
		UploadRateLimiter:              unlimited,
		DisableAcceptRateLimiting:      true,
		DropMutuallyCompletePeers:      true,
		HeaderObfuscationPolicy: HeaderObfuscationPolicy{
			Preferred:        true,
			RequirePreferred: false,
		},
		CryptoSelector:         mse.DefaultCryptoSelector,
		CryptoProvides:         mse.AllSupportedCrypto,
		ListenPort:             42069,
		Extensions:             defaultPeerExtensionBytes(),
		DialForPeerConns:       true,
		AcceptPeerConnections:  true,
		MaxUnverifiedBytes:     64 << 20,
		DialRateLimiter:        rate.NewLimiter(10, 10),
		PieceHashersPerTorrent: 2,
	}
	cc.DhtStartingNodes = func(network string) dht.StartingNodesGetter {
		return func() ([]dht.Addr, error) { return dht.GlobalBootstrapAddrs(network) }
	}
	cc.PeriodicallyAnnounceTorrentsToDht = true
	cc.MetainfoSourcesMerger = func(t *Torrent, info *metainfo.MetaInfo) error {
		return t.MergeSpec(TorrentSpecFromMetaInfo(info))
	}
	return cc
}

type HeaderObfuscationPolicy struct {
	RequirePreferred bool // Whether the value of Preferred is a strict requirement.
	Preferred        bool // Whether header obfuscation is preferred.
}

func (cfg *ClientConfig) setRateLimiterBursts() {
	// What about chunk size?
	if cfg.UploadRateLimiter.Burst() == 0 {
		cfg.UploadRateLimiter.SetBurst(cfg.MaxAllocPeerRequestDataPerConn)
	}
	setDefaultDownloadRateLimiterBurstIfZero(cfg.DownloadRateLimiter)
}

// Returns the download rate.Limit handling the special nil case.
func EffectiveDownloadRateLimit(l *rate.Limiter) rate.Limit {
	if l == nil {
		return rate.Inf
	}
	return l.Limit()
}

type MetainfoSourcesConfig struct {
	// Used for torrent metainfo sources only. Falls back to the http.Client created to wrap
	// WebTransport.
	MetainfoSourcesClient *http.Client
	// If a sources successfully fetches metainfo, this function is called to apply the metainfo. t
	// is provided to prevent a race as the fetcher for the source was bound to it. Returning an
	// error will kill the respective sourcer.
	MetainfoSourcesMerger func(t *Torrent, info *metainfo.MetaInfo) error
}
