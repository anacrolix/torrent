package torrent

type pendingRequests struct {
	m map[RequestIndex]int
}

func (p pendingRequests) Dec(r RequestIndex) {
	p.m[r]--
	n := p.m[r]
	if n == 0 {
		delete(p.m, r)
	}
	if n < 0 {
		panic(n)
	}
}

func (p pendingRequests) Inc(r RequestIndex) {
	p.m[r]++
}

func (p *pendingRequests) Init() {
	p.m = make(map[RequestIndex]int)
}

func (p *pendingRequests) AssertEmpty() {
	if len(p.m) != 0 {
		panic(p.m)
	}
}

func (p pendingRequests) Get(r RequestIndex) int {
	return p.m[r]
}
