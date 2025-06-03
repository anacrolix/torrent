#!/usr/bin/env bash
echo BASH_VERSION="$BASH_VERSION"
set -eux
repopath="$(cd "$(dirname "$0")/.."; pwd)"
debian_file=debian-10.8.0-amd64-netinst.iso
mkdir -p mnt torrents
# I think the timing can cause torrents to not get added correctly to the torrentfs client, so add
# them first and start the fs afterwards.
pushd torrents
cp "$repopath/testdata/$debian_file.torrent" .
godo -v -- "$repopath/cmd/torrent" metainfo "$repopath/testdata/sintel.torrent" magnet > sintel.magnet
popd
#file="$debian_file"
file=Sintel/Sintel.mp4

GOPPROF=http godo -v -- "$repopath/fs/cmd/torrentfs" -mountDir=mnt -metainfoDir=torrents &> torrentfs.log &
torrentfs_pid=$!
trap 'kill "$torrentfs_pid"' EXIT

check_file() {
	while [ ! -e "mnt/$file" ]; do sleep 1; done
	pv -f "mnt/$file" | gmd5sum -c <(cat <<-EOF
	083e808d56aa7b146f513b3458658292  -
	EOF
	)
}

( check_file ) &
check_file_pid=$!

trap 'kill "$torrentfs_pid" "$check_file_pid"' EXIT
wait -n
status=$?
sudo umount mnt
trap - EXIT
echo "wait returned" $status
exit $status
