module github.com/anacrolix/torrent

go 1.16

require (
	github.com/RoaringBitmap/roaring v0.9.4
	github.com/alexflint/go-arg v1.4.2
	github.com/anacrolix/args v0.4.1-0.20211104085705-59f0fe94eb8f
	github.com/anacrolix/chansync v0.3.0
	github.com/anacrolix/confluence v1.9.0 // indirect
	github.com/anacrolix/dht/v2 v2.14.1-0.20211220010335-4062f7927abf
	github.com/anacrolix/envpprof v1.1.1
	github.com/anacrolix/fuse v0.2.0
	github.com/anacrolix/go-libutp v1.1.0
	github.com/anacrolix/log v0.10.0
	github.com/anacrolix/missinggo v1.3.0
	github.com/anacrolix/missinggo/perf v1.0.0
	github.com/anacrolix/missinggo/v2 v2.5.2
	github.com/anacrolix/multiless v0.2.0
	github.com/anacrolix/squirrel v0.2.1-0.20211119092713-2efaee06d169
	github.com/anacrolix/sync v0.4.0
	github.com/anacrolix/tagflag v1.3.0
	github.com/anacrolix/upnp v0.1.2-0.20200416075019-5e9378ed1425
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
	github.com/pion/datachannel v1.4.21
	github.com/pion/ice/v2 v2.1.12 // indirect
	github.com/pion/interceptor v0.0.15 // indirect
	github.com/pion/rtp v1.7.2 // indirect
	github.com/pion/srtp/v2 v2.0.5 // indirect
	github.com/pion/webrtc/v3 v3.0.32
	github.com/pkg/errors v0.9.1
	github.com/stretchr/testify v1.7.0
	go.etcd.io/bbolt v1.3.6
	golang.org/x/crypto v0.0.0-20210813211128-0a44fdfbc16e // indirect
	golang.org/x/net v0.0.0-20210813160813-60bc85c4be6d // indirect
	golang.org/x/sys v0.0.0-20211023085530-d6a326fbbf70 // indirect
	golang.org/x/time v0.0.0-20210723032227-1f47c861a9ac
	zombiezen.com/go/sqlite v0.8.0
)

require (
	github.com/alexflint/go-scalar v1.1.0 // indirect
	github.com/anacrolix/mmsg v1.0.0 // indirect
	github.com/anacrolix/stm v0.3.0 // indirect
	github.com/benbjohnson/immutable v0.3.0 // indirect
	github.com/bits-and-blooms/bitset v1.2.0 // indirect
	github.com/google/uuid v1.3.0 // indirect
	github.com/huandu/xstrings v1.3.2 // indirect
	github.com/kr/pretty v0.3.0 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/mattn/go-isatty v0.0.12 // indirect
	github.com/mschoch/smat v0.2.0 // indirect
	github.com/pion/dtls/v2 v2.0.9 // indirect
	github.com/pion/logging v0.2.2 // indirect
	github.com/pion/mdns v0.0.5 // indirect
	github.com/pion/randutil v0.1.0 // indirect
	github.com/pion/rtcp v1.2.6 // indirect
	github.com/pion/sctp v1.7.12 // indirect
	github.com/pion/sdp/v3 v3.0.4 // indirect
	github.com/pion/stun v0.3.5 // indirect
	github.com/pion/transport v0.12.3 // indirect
	github.com/pion/turn/v2 v2.0.5 // indirect
	github.com/pion/udp v0.1.1 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20200410134404-eec4a21b6bb0 // indirect
	github.com/rogpeppe/go-internal v1.8.0 // indirect
	github.com/rs/dnscache v0.0.0-20210201191234-295bba877686 // indirect
	github.com/ryszard/goskiplist v0.0.0-20150312221310-2dfbae5fcf46 // indirect
	golang.org/x/sync v0.0.0-20210220032951-036812b2e83c // indirect
	golang.org/x/xerrors v0.0.0-20200804184101-5ec99f83aff1 // indirect
	gopkg.in/yaml.v3 v3.0.0-20200313102051-9f266ea9e77c // indirect
	modernc.org/libc v1.11.82 // indirect
	modernc.org/mathutil v1.4.1 // indirect
	modernc.org/memory v1.0.5 // indirect
	// https://gitlab.com/cznic/sqlite/-/issues/77#note_744477407
	modernc.org/sqlite v1.14.2-0.20211125151325-d4ed92c0a70f // indirect
)

retract (
	// Doesn't signal interest to peers if choked when piece priorities change.
	v1.39.0
	// peer-requesting doesn't scale
	[v1.34.0, v1.38.1]
)
