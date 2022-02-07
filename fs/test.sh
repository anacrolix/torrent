set -eux
repopath="$(cd "$(dirname "$0")/.."; pwd)"
debian_file=debian-10.8.0-amd64-netinst.iso
mkdir -p mnt torrents
# I think the timing can cause torrents to not get added correctly to the torrentfs client, so add
# them first and start the fs afterwards.
pushd torrents
cp "$repopath/testdata/$debian_file.torrent" .
godo -v "$repopath/cmd/torrent" metainfo "$repopath/testdata/sintel.torrent" magnet > sintel.magnet
popd
GOPPROF=http godo -v "$repopath/cmd/torrentfs" -mountDir=mnt -metainfoDir=torrents &
trap 'set +e; sudo umount -f mnt' EXIT
#file="$debian_file"
file=Sintel/Sintel.mp4
while [ ! -e "mnt/$file" ]; do sleep 1; done
pv -f "mnt/$file" | md5sum
# expect e221f43f4fdd409250908fc4305727d4
sudo umount mnt
wait || echo "wait returned" $?
