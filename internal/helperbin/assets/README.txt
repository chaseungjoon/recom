Build-time location for embedded Linux helper binaries.

`make helpers` (and the GoReleaser before-hook) writes
recom-helper-linux-amd64.gz and recom-helper-linux-arm64.gz here before the
client is compiled. They are not committed to git.
