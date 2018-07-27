set -ex
repopath=$(cd $(dirname $0)/..; pwd)
d=$(mktemp -d)
pushd "$d"
mkdir mnt torrents
GOPPROF=http torrentfs -mountDir=mnt -metainfoDir=torrents &
trap 'set +e; sudo umount -f mnt; pushd; rm -rv "$d"' EXIT
pushd torrents
cp "$repopath"/testdata/debian-9.1.0-amd64-netinst.iso.torrent .
echo 'magnet:?xt=urn:btih:6a9759bffd5c0af65319979fb7832189f4f3c35d&dn=sintel.mp4' > sintel.magnet
popd
file=debian-9.1.0-amd64-netinst.iso
# file=sintel.mp4
while [ ! -e "mnt/$file" ]; do sleep 1; done
pv "mnt/$file" | md5sum
sudo umount mnt
wait || echo "wait returned" $?
