module github.com/anacrolix/torrent

require (
	bazil.org/fuse v0.0.0-20200407214033-5883e5a4b512
	crawshaw.io/sqlite v0.3.3-0.20201116044518-95be3f88ee0f
	github.com/RoaringBitmap/roaring v0.5.5 // indirect
	github.com/alangpierce/go-forceexport v0.0.0-20160317203124-8f1d6941cd75 // indirect
	github.com/alexflint/go-arg v1.3.0
	github.com/anacrolix/dht/v2 v2.7.1
	github.com/anacrolix/envpprof v1.1.0
	github.com/anacrolix/go-libutp v1.0.4
	github.com/anacrolix/log v0.7.1-0.20200604014615-c244de44fd2d
	github.com/anacrolix/missinggo v1.2.1
	github.com/anacrolix/missinggo/perf v1.0.0
	github.com/anacrolix/missinggo/v2 v2.5.0
	github.com/anacrolix/multiless v0.0.0-20200413040533-acfd16f65d5d
	github.com/anacrolix/sync v0.2.0
	github.com/anacrolix/tagflag v1.1.1-0.20200411025953-9bb5209d56c2
	github.com/anacrolix/upnp v0.1.2-0.20200416075019-5e9378ed1425
	github.com/anacrolix/utp v0.0.0-20180219060659-9e0e1d1d0572
	github.com/benbjohnson/immutable v0.3.0 // indirect
	github.com/bradfitz/iter v0.0.0-20191230175014-e8f45d346db8
	github.com/davecgh/go-spew v1.1.1
	github.com/dustin/go-humanize v1.0.0
	github.com/edsrzf/mmap-go v1.0.0
	github.com/elliotchance/orderedmap v1.3.0
	github.com/frankban/quicktest v1.11.3
	github.com/fsnotify/fsnotify v1.4.9
	github.com/golang/protobuf v1.4.3 // indirect
	github.com/golang/snappy v0.0.2 // indirect
	github.com/google/btree v1.0.0
	github.com/google/uuid v1.1.5 // indirect
	github.com/gorilla/websocket v1.4.2
	github.com/huandu/xstrings v1.3.2 // indirect
	github.com/jessevdk/go-flags v1.4.0
	github.com/lucas-clemente/quic-go v0.19.3 // indirect
	github.com/pion/datachannel v1.4.21
	github.com/pion/dtls/v2 v2.0.4 // indirect
	github.com/pion/quic v0.1.4 // indirect
	github.com/pion/rtcp v1.2.6 // indirect
	github.com/pion/rtp v1.6.2 // indirect
	github.com/pion/sctp v1.7.11 // indirect
	github.com/pion/srtp v1.5.2 // indirect
	github.com/pion/transport v0.12.2 // indirect
	github.com/pion/turn/v2 v2.0.5 // indirect
	github.com/pion/webrtc/v2 v2.2.26
	github.com/pkg/errors v0.9.1
	github.com/stretchr/testify v1.6.1
	github.com/tinylib/msgp v1.1.5 // indirect
	github.com/willf/bitset v1.1.11 // indirect
	go.etcd.io/bbolt v1.3.5
	golang.org/x/crypto v0.0.0-20201221181555-eec23a3978ad // indirect
	golang.org/x/net v0.0.0-20201224014010-6772e930b67b // indirect
	golang.org/x/sync v0.0.0-20201207232520-09787c993a3a // indirect
	golang.org/x/sys v0.0.0-20210113181707-4bcb84eeeb78 // indirect
	golang.org/x/time v0.0.0-20201208040808-7e3f01d25324
	golang.org/x/xerrors v0.0.0-20200804184101-5ec99f83aff1
	google.golang.org/protobuf v1.25.0 // indirect
)

go 1.15

exclude bazil.org/fuse v0.0.0-20200419173433-3ba628eaf417

replace crawshaw.io/sqlite => github.com/getlantern/sqlite v0.3.3-0.20201116012831-1a85f453b62f
