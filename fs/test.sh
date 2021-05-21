set -eux
repopath="$(cd "$(dirname "$0")/.."; pwd)"
mkdir -p mnt torrents
GOPPROF=http godo -v "$repopath/cmd/torrentfs" -mountDir=mnt -metainfoDir=torrents &
trap 'set +e; sudo umount -f mnt' EXIT
debian_file=debian-10.8.0-amd64-netinst.iso
pushd torrents
cp "$repopath/testdata/$debian_file.torrent" .
echo 'magnet:?xt=urn:btih:6a9759bffd5c0af65319979fb7832189f4f3c35d&dn=sintel.mp4' > sintel.magnet
popd
file="$debian_file"
# file=sintel.mp4
while [ ! -e "mnt/$file" ]; do sleep 1; done
pv -f "mnt/$file" | md5sum
# expect e221f43f4fdd409250908fc4305727d4
sudo umount mnt
wait || echo "wait returned" $?
