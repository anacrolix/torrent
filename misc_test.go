package torrent

import . "gopkg.in/check.v1"

func (suite) TestTorrentOffsetRequest(c *C) {
	check := func(tl, ps, off int64, expected request, ok bool) {
		req, _ok := torrentOffsetRequest(tl, ps, defaultChunkSize, off)
		c.Check(_ok, Equals, ok)
		c.Check(req, Equals, expected)
	}
	check(13, 5, 0, newRequest(0, 0, 5), true)
	check(13, 5, 3, newRequest(0, 0, 5), true)
	check(13, 5, 11, newRequest(2, 0, 3), true)
	check(13, 5, 13, request{}, false)
}
