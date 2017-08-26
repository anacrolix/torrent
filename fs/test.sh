mkdir mnt torrents
GOPPROF=http godo github.com/anacrolix/torrent/cmd/torrentfs -mountDir mnt -torrentPath torrents &
cd torrents
wget http://releases.ubuntu.com/14.04.2/ubuntu-14.04.2-desktop-amd64.iso.torrent
cd ..
ls mnt
pv mnt/ubuntu-14.04.2-desktop-amd64.iso | md5sum
