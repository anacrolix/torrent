mkdir mnt torrents
GOPPROF=http godo github.com/anacrolix/torrent/cmd/torrentfs -mountDir=mnt -metainfoDir=torrents &
cd torrents
wget http://releases.ubuntu.com/14.04.2/ubuntu-14.04.2-desktop-amd64.iso.torrent
cd ..
file=ubuntu-14.04.2-desktop-amd64.iso
while [ ! -e "mnt/$file" ]; do sleep 1; done
pv "mnt/$file" | md5sum
