module github.com/anacrolix/torrent

go 1.16

require (
	github.com/RoaringBitmap/roaring v0.9.4
	github.com/alexflint/go-arg v1.4.2
	github.com/anacrolix/args v0.4.1-0.20211104085705-59f0fe94eb8f
	github.com/anacrolix/chansync v0.3.0
	github.com/anacrolix/dht/v2 v2.15.2-0.20220123034220-0538803801cb
	github.com/anacrolix/envpprof v1.1.1
	github.com/anacrolix/fuse v0.2.0
	github.com/anacrolix/go-libutp v1.2.0
	github.com/anacrolix/log v0.10.1-0.20220123034749-3920702c17f8
	github.com/anacrolix/missinggo v1.3.0
	github.com/anacrolix/missinggo/perf v1.0.0
	github.com/anacrolix/missinggo/v2 v2.5.2
	github.com/anacrolix/multiless v0.2.0
	github.com/anacrolix/squirrel v0.4.0
	github.com/anacrolix/sync v0.4.0
	github.com/anacrolix/tagflag v1.3.0
	github.com/anacrolix/upnp v0.1.3-0.20220123035249-922794e51c96
	github.com/anacrolix/utp v0.1.0
	github.com/bradfitz/iter v0.0.0-20191230175014-e8f45d346db8
	github.com/davecgh/go-spew v1.1.1
	github.com/dustin/go-humanize v1.0.0
	github.com/edsrzf/mmap-go v1.0.0
	github.com/elliotchance/orderedmap v1.4.0
	github.com/frankban/quicktest v1.14.0
	github.com/fsnotify/fsnotify v1.5.1
	github.com/google/btree v1.0.1
	github.com/google/go-cmp v0.5.6
	github.com/gorilla/websocket v1.4.2
	github.com/jessevdk/go-flags v1.5.0
	github.com/pion/datachannel v1.5.2
	github.com/pion/webrtc/v3 v3.1.24-0.20220208053747-94262c1b2b38
	github.com/pkg/errors v0.9.1
	github.com/stretchr/testify v1.7.0
	go.etcd.io/bbolt v1.3.6
	golang.org/x/time v0.0.0-20210723032227-1f47c861a9ac
	zombiezen.com/go/sqlite v0.8.0
)

retract (
	// Doesn't signal interest to peers if choked when piece priorities change.
	v1.39.0
	// peer-requesting doesn't scale
	[v1.34.0, v1.38.1]
)
