repopath=$(cd $(dirname $0)/..; pwd)
mkdir mnt torrents
umount mnt
set -e
GOPPROF=http godo github.com/anacrolix/torrent/cmd/torrentfs -mountDir=mnt -metainfoDir=torrents &
cd torrents
cp "$repopath"/testdata/debian-9.1.0-amd64-netinst.iso.torrent .
echo 'magnet:?xt=urn:btih:6a9759bffd5c0af65319979fb7832189f4f3c35d&dn=sintel.mp4' > sintel.magnet
cd ..
file=debian-9.1.0-amd64-netinst.iso
# file=sintel.mp4
while [ ! -e "mnt/$file" ]; do sleep 1; done
pv "mnt/$file" | md5sum
sudo umount mnt
wait
