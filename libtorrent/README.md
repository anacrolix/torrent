# libtorrent

## Headers

`# go tool cgo libtorrent.go`

## Android

Use within your android gralde project:

```bash
#!/bin/bash
#
# https://groups.google.com/forum/#!topic/go-mobile/ZstjAiIFrWY
#

MODULE=libtorrent

go get -u github.com/anacrolix/torrent || exit 1

mkdir -p "$MODULE" || exit 1

cat > "$MODULE/build.gradle" << EOF
configurations.maybeCreate("default")
artifacts.add("default", file('libtorrent.aar'))
EOF

gomobile bind -o "$MODULE/libtorrent.aar" github.com/anacrolix/torrent/libtorrent || exit 1
```

Then import your libtorrent.arr into Android Studio or Eclipse.
