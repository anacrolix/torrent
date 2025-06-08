package torrent

import (
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/james-lawrence/torrent/connections"
	"github.com/james-lawrence/torrent/dht"
	"github.com/james-lawrence/torrent/dht/krpc"
	"github.com/james-lawrence/torrent/internal/netx"
	"github.com/james-lawrence/torrent/metainfo"
	"github.com/james-lawrence/torrent/storage"
	"github.com/james-lawrence/torrent/tracker"
	"github.com/james-lawrence/torrent/x/conntrack"
	"golang.org/x/time/rate"

	"github.com/james-lawrence/torrent/mse"
)

// DefaultHTTPUserAgent ...
const DefaultHTTPUserAgent = "Go-Torrent/1.0"

// ClientConfig not safe to modify this after it's given to a Client.
type ClientConfig struct {
	defaultStorage  storage.ClientImpl
	defaultMetadata MetadataStore

	defaultPortForwarding bool
	UpnpID                string

	DisablePEX bool `long:"disable-pex"`

	// Don't create a DHT.
	NoDHT            bool `long:"disable-dht"`
	DhtStartingNodes func(network string) dht.StartingNodesGetter
	// Never send chunks to peers.
	NoUpload bool `long:"no-upload"`

	// Upload even after there's nothing in it for us. By default uploading is
	// not altruistic, we'll only upload to encourage the peer to reciprocate.
	Seed bool `long:"seed"`
	// Only applies to chunks uploaded to peers, to maintain responsiveness
	// communicating local Client state to peers. Each limiter token
	// represents one byte. The Limiter's burst must be large enough to fit a
	// whole chunk, which is usually 16 KiB (see TorrentSpec.ChunkSize).
	UploadRateLimiter *rate.Limiter
	// Rate limits all reads from connections to peers. Each limiter token
	// represents one byte. The Limiter's burst must be bigger than the
	// largest Read performed on a the underlying rate-limiting io.Reader
	// minus one. This is likely to be the larger of the main read loop buffer
	// (~4096), and the requested chunk size (~16KiB, see
	// TorrentSpec.ChunkSize).
	DownloadRateLimiter *rate.Limiter

	// Rate limit connection dialing
	dialRateLimiter *rate.Limiter

	bucketLimit int // maximum number of peers per bucket in the DHT.

	// User-provided Client peer ID. If not present, one is generated automatically.
	PeerID string

	HeaderObfuscationPolicy HeaderObfuscationPolicy
	// The crypto methods to offer when initiating connections with header obfuscation.
	CryptoProvides mse.CryptoMethod
	// Chooses the crypto method to use when receiving connections with header obfuscation.
	CryptoSelector mse.CryptoSelector

	DisableIPv4Peers bool
	Logger           logger // standard logging for errors, defaults to stderr
	Warn             logger // warn logging
	Debug            logger // debug logging, defaults to discard

	// HTTPProxy defines proxy for HTTP requests.
	// Format: func(*Request) (*url.URL, error),
	// or result of http.ProxyURL(HTTPProxy).
	// By default, it is composed from ClientConfig.ProxyURL,
	// if not set explicitly in ClientConfig struct
	HTTPProxy func(*http.Request) (*url.URL, error)
	// HTTPUserAgent changes default UserAgent for HTTP requests
	HTTPUserAgent string
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
	MinDialTimeout          time.Duration
	HalfOpenConnsPerTorrent int
	// Maximum number of peer addresses in reserve.
	TorrentPeersHighWater int
	// Minumum number of peers before effort is made to obtain more peers.
	TorrentPeersLowWater int

	// Limit how long handshake can take. This is to reduce the lingering
	// impact of a few bad apples. 4s loses 1% of successful handshakes that
	// are obtained with 60s timeout, and 5% of unsuccessful handshakes.
	HandshakesTimeout time.Duration

	dialer netx.Dialer
	// The IP addresses as our peers should see them. May differ from the
	// local interfaces due to NAT or other network configurations.
	publicIP4 net.IP
	publicIP6 net.IP

	// Don't add connections that have the same peer ID as an existing
	// connection for a given Torrent.
	dropDuplicatePeerIds bool

	ConnTracker *conntrack.Instance

	connections.Handshaker

	// OnQuery hook func
	DHTOnQuery      func(query *krpc.Msg, source net.Addr) (propagate bool)
	DHTAnnouncePeer func(ih metainfo.Hash, ip net.IP, port int, portOk bool)
	DHTMuxer        dht.Muxer

	ConnectionClosed func(ih metainfo.Hash, stats ConnStats)
}

func (cfg *ClientConfig) Storage() storage.ClientImpl {
	return cfg.defaultStorage
}

func (cfg *ClientConfig) PublicIP4() net.IP {
	return cfg.publicIP4
}

func (cfg *ClientConfig) PublicIP6() net.IP {
	return cfg.publicIP6
}

func (cfg *ClientConfig) AnnounceRequest() tracker.Announce {
	return tracker.Announce{
		UserAgent: cfg.HTTPUserAgent,
		ClientIp4: krpc.NewNodeAddrFromIPPort(cfg.publicIP4, 0),
		ClientIp6: krpc.NewNodeAddrFromIPPort(cfg.publicIP6, 0),
		Dialer:    cfg.dialer,
	}
}

func (cfg *ClientConfig) errors() llog {
	return llog{logger: cfg.Logger}
}

func (cfg *ClientConfig) warn() llog {
	return llog{logger: cfg.Warn}
}

func (cfg *ClientConfig) info() llog {
	return llog{logger: cfg.Logger}
}

func (cfg *ClientConfig) debug() llog {
	return llog{logger: cfg.Debug}
}

// ClientConfigOption options for the client configuration
type ClientConfigOption func(*ClientConfig)

// useful for default noop configurations.
func ClientConfigNoop(c *ClientConfig) {}

func ClientConfigCompose(options ...ClientConfigOption) ClientConfigOption {
	return func(cc *ClientConfig) {
		for _, opt := range options {
			opt(cc)
		}
	}
}

func ClientConfigDialer(d netx.Dialer) ClientConfigOption {
	return func(cc *ClientConfig) {
		cc.dialer = d
	}
}

func ClientConfigDHTEnabled(b bool) ClientConfigOption {
	return func(cc *ClientConfig) {
		cc.NoDHT = !b
	}
}

func ClientConfigPortForward(b bool) ClientConfigOption {
	return func(cc *ClientConfig) {
		cc.defaultPortForwarding = b
	}
}

func ClientConfigIPv4(ip string) ClientConfigOption {
	return func(cc *ClientConfig) {
		if len(ip) == 0 {
			return
		}

		cc.publicIP4 = net.ParseIP(ip)
	}
}

func ClientConfigIPv6(ip string) ClientConfigOption {
	return func(cc *ClientConfig) {
		if len(ip) == 0 {
			return
		}

		cc.publicIP6 = net.ParseIP(ip)
	}
}

func ClientConfigDialRateLimit(l *rate.Limiter) ClientConfigOption {
	return func(cc *ClientConfig) {
		cc.dialRateLimiter = l
	}
}

func ClientConfigBucketLimit(i int) ClientConfigOption {
	return func(cc *ClientConfig) {
		cc.bucketLimit = i
	}
}

// ClientConfigInfoLogger set the info logger
func ClientConfigInfoLogger(l *log.Logger) ClientConfigOption {
	return func(c *ClientConfig) {
		c.Logger = l
	}
}

func ClientConfigDebugLogger(l *log.Logger) ClientConfigOption {
	return func(c *ClientConfig) {
		c.Debug = l
	}
}

// ClientConfigSeed enable/disable seeding
func ClientConfigSeed(b bool) ClientConfigOption {
	return func(c *ClientConfig) {
		c.Seed = b
	}
}

func ClientConfigStorage(s storage.ClientImpl) ClientConfigOption {
	return func(c *ClientConfig) {
		c.defaultStorage = s
	}
}

func ClientConfigStorageDir(dir string) ClientConfigOption {
	return func(c *ClientConfig) {
		c.defaultStorage = storage.NewFile(dir)
		c.defaultMetadata = NewMetadataCache(dir)
	}
}

// configure what endpoints the dht's will support.
func ClientConfigMuxer(m dht.Muxer) ClientConfigOption {
	return func(c *ClientConfig) {
		c.DHTMuxer = m
	}
}

func ClientConfigPeerID(s string) ClientConfigOption {
	return func(c *ClientConfig) {
		c.PeerID = s
	}
}

func ClientConfigBootstrapFn(fn func(n string) dht.StartingNodesGetter) ClientConfigOption {
	return func(c *ClientConfig) {
		c.DhtStartingNodes = fn
	}
}

func ClientConfigBootstrapGlobal(c *ClientConfig) {
	c.DhtStartingNodes = func(network string) dht.StartingNodesGetter {
		return func() ([]dht.Addr, error) { return dht.GlobalBootstrapAddrs(network) }
	}
}

// Bootstrap from a file written by dht.WriteNodesToFile
func ClientConfigBootstrapPeerFile(path string) ClientConfigOption {
	return ClientConfigBootstrapFn(func(n string) dht.StartingNodesGetter {
		return func() (res []dht.Addr, err error) {
			ps, err := dht.ReadNodesFromFile(n)
			if err != nil {
				return nil, err
			}

			for _, p := range ps {
				res = append(res, dht.NewAddr(p.Addr.UDP()))
			}

			return res, nil
		}
	})
}

func ClientConfigHTTPUserAgent(s string) ClientConfigOption {
	return func(cc *ClientConfig) {
		cc.HTTPUserAgent = s
	}
}

func ClientConfigConnectionClosed(fn func(ih metainfo.Hash, stats ConnStats)) ClientConfigOption {
	return func(cc *ClientConfig) {
		cc.ConnectionClosed = fn
	}
}

// NewDefaultClientConfig default client configuration.
func NewDefaultClientConfig(mdstore MetadataStore, store storage.ClientImpl, options ...ClientConfigOption) *ClientConfig {
	cc := &ClientConfig{
		defaultMetadata:                mdstore,
		defaultStorage:                 store,
		HTTPUserAgent:                  DefaultHTTPUserAgent,
		ExtendedHandshakeClientVersion: "go.torrent dev 20181121",
		Bep20:                          "-GT0002-",
		UpnpID:                         "james-lawrence/torrent",
		NominalDialTimeout:             20 * time.Second,
		MinDialTimeout:                 3 * time.Second,
		HalfOpenConnsPerTorrent:        25,
		TorrentPeersHighWater:          500,
		TorrentPeersLowWater:           50,
		HandshakesTimeout:              4 * time.Second,
		defaultPortForwarding:          true,
		DhtStartingNodes: func(network string) dht.StartingNodesGetter {
			return func() ([]dht.Addr, error) { return nil, nil }
		},
		UploadRateLimiter:   rate.NewLimiter(rate.Inf, 0),
		DownloadRateLimiter: rate.NewLimiter(rate.Inf, 0),
		dialRateLimiter:     rate.NewLimiter(rate.Inf, 0),
		ConnTracker:         conntrack.NewInstance(),
		HeaderObfuscationPolicy: HeaderObfuscationPolicy{
			Preferred:        true,
			RequirePreferred: false,
		},
		CryptoSelector:   mse.DefaultCryptoSelector,
		CryptoProvides:   mse.AllSupportedCrypto,
		Logger:           log.New(io.Discard, "[torrent] ", log.Flags()),
		Warn:             discard{},
		Debug:            discard{},
		DHTAnnouncePeer:  func(ih metainfo.Hash, ip net.IP, port int, portOk bool) {},
		DHTMuxer:         dht.DefaultMuxer(),
		ConnectionClosed: func(t metainfo.Hash, stats ConnStats) {},
		Handshaker: connections.NewHandshaker(
			connections.AutoFirewall(),
		),
	}

	for _, opt := range options {
		opt(cc)
	}

	return cc
}

// HeaderObfuscationPolicy ...
type HeaderObfuscationPolicy struct {
	RequirePreferred bool // Whether the value of Preferred is a strict requirement.
	Preferred        bool // Whether header obfuscation is preferred.
}
