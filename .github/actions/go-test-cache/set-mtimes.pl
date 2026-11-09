#!/usr/bin/env perl
# Give every file git tracks (including in submodules) an mtime derived from
# its content, and every directory containing them a fixed mtime.
#
# go test caches a package's result keyed on, among other things, the size and
# mtime of each file inside the module that the test opened or stat'ed. A fresh
# checkout sets every mtime to "now", so any test that reads testdata would miss
# the cache on every run. Deriving the mtime from the git blob hash keeps it
# stable while the content is unchanged, and changes it when the content does.
#
# Run from the repository root, after checkout.
use strict;
use warnings;

# Must be well in the past: go test refuses to cache results that depend on
# files modified in the last 2 seconds.
my $base = 1_000_000_000;    # 2001-09-09

open(my $ls, '-|', 'git', 'ls-files', '-z', '--stage', '--recurse-submodules')
    or die "git ls-files: $!";
local $/ = "\0";
my %dirs = ('.' => 1);
my $n = 0;
while (my $entry = <$ls>) {
    chomp $entry;
    my ($mode, $sha, undef, $path) = $entry =~ /^(\d+) ([0-9a-f]+) (\d)\t(.*)$/s
        or die "unexpected git ls-files line: $entry";
    # Regular files only: skip symlinks (utime would follow them) and gitlinks.
    next unless $mode =~ /^100/;
    next unless -f $path;
    my $mtime = $base + hex(substr($sha, 0, 7));
    utime($mtime, $mtime, $path) or die "utime $path: $!";
    $n++;
    my $dir = $path;
    while ($dir =~ s{/[^/]*$}{}) {
        last if $dirs{$dir}++;
    }
}
close($ls) or die "git ls-files failed: $?";

# Directory mtimes change whenever an entry is added or removed, but go test
# also hashes the name and stat of every entry when a test reads a directory,
# so a fixed directory mtime loses nothing. Failing to set one only costs cache
# hits, so ignore errors.
utime($base, $base, $_) for keys %dirs;

printf "set mtimes on %d files and %d directories\n", $n, scalar keys %dirs;
