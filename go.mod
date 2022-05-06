module github.com/anacrolix/torrent

go 1.18

require (
	crawshaw.io/sqlite v0.3.3-0.20210127221821-98b1f83c5508
	github.com/RoaringBitmap/roaring v0.9.4
	github.com/ajwerner/btree v0.0.0-20211201061316-91c8b66ad617
	github.com/alexflint/go-arg v1.4.2
	github.com/anacrolix/args v0.5.0
	github.com/anacrolix/chansync v0.3.0
	github.com/anacrolix/dht/v2 v2.16.2-0.20220311024416-dd658f18fd51
	github.com/anacrolix/envpprof v1.2.1
	github.com/anacrolix/fuse v0.2.0
	github.com/anacrolix/generics v0.0.0-20220217222028-44932cf46edd
	github.com/anacrolix/go-libutp v1.2.0
	github.com/anacrolix/log v0.13.1
	github.com/anacrolix/missinggo v1.3.0
	github.com/anacrolix/missinggo/perf v1.0.0
	github.com/anacrolix/missinggo/v2 v2.7.0
	github.com/anacrolix/multiless v0.2.1-0.20211218050420-533661eef5dc
	github.com/anacrolix/squirrel v0.4.1-0.20220122230132-14b040773bac
	github.com/anacrolix/sync v0.4.0
	github.com/anacrolix/tagflag v1.3.0
	github.com/anacrolix/upnp v0.1.3-0.20220123035249-922794e51c96
	github.com/anacrolix/utp v0.1.0
	github.com/bahlo/generic-list-go v0.2.0
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
	github.com/lispad/go-generics-tools v1.0.0
	github.com/pion/datachannel v1.5.2
	github.com/pion/logging v0.2.2
	github.com/pion/webrtc/v3 v3.1.24-0.20220208053747-94262c1b2b38
	github.com/pkg/errors v0.9.1
	github.com/prometheus/client_golang v1.5.1
	github.com/stretchr/testify v1.7.1
	github.com/tidwall/btree v0.7.2-0.20211211132910-4215444137fc
	go.etcd.io/bbolt v1.3.6
	golang.org/x/time v0.0.0-20210723032227-1f47c861a9ac
)

require (
	github.com/alexflint/go-scalar v1.1.0 // indirect
	github.com/anacrolix/mmsg v1.0.0 // indirect
	github.com/anacrolix/stm v0.3.0 // indirect
	github.com/benbjohnson/immutable v0.3.0 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/bits-and-blooms/bitset v1.2.0 // indirect
	github.com/cespare/xxhash/v2 v2.1.1 // indirect
	github.com/golang/protobuf v1.5.2 // indirect
	github.com/google/uuid v1.3.0 // indirect
	github.com/huandu/xstrings v1.3.2 // indirect
	github.com/kr/pretty v0.3.0 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/matttproud/golang_protobuf_extensions v1.0.1 // indirect
	github.com/mschoch/smat v0.2.0 // indirect
	github.com/pion/dtls/v2 v2.1.2 // indirect
	github.com/pion/ice/v2 v2.1.20 // indirect
	github.com/pion/interceptor v0.1.7 // indirect
	github.com/pion/mdns v0.0.5 // indirect
	github.com/pion/randutil v0.1.0 // indirect
	github.com/pion/rtcp v1.2.9 // indirect
	github.com/pion/rtp v1.7.4 // indirect
	github.com/pion/sctp v1.8.2 // indirect
	github.com/pion/sdp/v3 v3.0.4 // indirect
	github.com/pion/srtp/v2 v2.0.5 // indirect
	github.com/pion/stun v0.3.5 // indirect
	github.com/pion/transport v0.13.0 // indirect
	github.com/pion/turn/v2 v2.0.6 // indirect
	github.com/pion/udp v0.1.1 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/prometheus/client_model v0.2.0 // indirect
	github.com/prometheus/common v0.9.1 // indirect
	github.com/prometheus/procfs v0.0.11 // indirect
	github.com/rogpeppe/go-internal v1.8.0 // indirect
	github.com/rs/dnscache v0.0.0-20210201191234-295bba877686 // indirect
	github.com/ryszard/goskiplist v0.0.0-20150312221310-2dfbae5fcf46 // indirect
	golang.org/x/crypto v0.0.0-20220131195533-30dcbda58838 // indirect
	golang.org/x/exp v0.0.0-20220328175248-053ad81199eb // indirect
	golang.org/x/net v0.0.0-20220127200216-cd36cc0744dd // indirect
	golang.org/x/sync v0.0.0-20210220032951-036812b2e83c // indirect
	golang.org/x/sys v0.0.0-20211216021012-1d35b9e2eb4e // indirect
	golang.org/x/xerrors v0.0.0-20200804184101-5ec99f83aff1 // indirect
	google.golang.org/protobuf v1.26.0 // indirect
	gopkg.in/yaml.v3 v3.0.0-20200313102051-9f266ea9e77c // indirect
)

retract (
	// Doesn't signal interest to peers if choked when piece priorities change.
	v1.39.0
	// peer-requesting doesn't scale
	[v1.34.0, v1.38.1]
)
