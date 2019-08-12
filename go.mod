module github.com/anacrolix/torrent

require (
	bazil.org/fuse v0.0.0-20180421153158-65cc252bf669
	github.com/RoaringBitmap/roaring v0.4.18 // indirect
	github.com/anacrolix/dht/v2 v2.0.1
	github.com/anacrolix/envpprof v0.0.0-20180404065416-323002cec2fa
	github.com/anacrolix/go-libutp v1.0.2
	github.com/anacrolix/log v0.2.0
	github.com/anacrolix/missinggo v1.1.0
	github.com/anacrolix/mmsg v1.0.0 // indirect
	github.com/anacrolix/sync v0.0.0-20180808010631-44578de4e778
	github.com/anacrolix/tagflag v0.0.0-20180803105420-3a8ff5428f76
	github.com/anacrolix/utp v0.0.0-20180219060659-9e0e1d1d0572
	github.com/boltdb/bolt v1.3.1
	github.com/bradfitz/iter v0.0.0-20190303215204-33e6a9893b0c
	github.com/davecgh/go-spew v1.1.1
	github.com/dustin/go-humanize v1.0.0
	github.com/edsrzf/mmap-go v1.0.0
	github.com/elgatito/upnp v0.0.0-20180711183757-2f244d205f9a
	github.com/fsnotify/fsnotify v1.4.7
	github.com/glycerine/goconvey v0.0.0-20190315024820-982ee783a72e // indirect
	github.com/google/btree v1.0.0
	github.com/gopherjs/gopherjs v0.0.0-20190309154008-847fc94819f9 // indirect
	github.com/gosuri/uilive v0.0.3 // indirect
	github.com/gosuri/uiprogress v0.0.1
	github.com/jessevdk/go-flags v1.4.0
	github.com/mattn/go-isatty v0.0.7 // indirect
	github.com/mattn/go-sqlite3 v1.10.0
	github.com/pkg/errors v0.8.1
	github.com/smartystreets/assertions v0.0.0-20190215210624-980c5ac6f3ac // indirect
	github.com/stretchr/testify v1.3.0
	github.com/golang/net v0.0.0-20190628185345-da137c7871d7
	github.com/golang/sys v0.0.0-20190712062909-fae7ac547cb7 // indirect
	github.com/golang/time v0.0.0-20190308202827-9d24e82272b4
	github.com/golang/xerrors v0.0.0-20190717185122-a985d3407aa7
)

go 1.13

replace github.com/elgatito/upnp => github.com/anacrolix/upnp v0.0.0-20190717072655-8249d7a81c03
