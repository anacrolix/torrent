package torrent

type pendingRequests struct {
	m []int
}

func (p *pendingRequests) Dec(r RequestIndex) {
	prev := p.m[r]
	if prev <= 0 {
		panic(prev)
	}
	p.m[r]--
}

func (p *pendingRequests) Inc(r RequestIndex) {
	p.m[r]++
}

func (p *pendingRequests) Init(maxIndex RequestIndex) {
	p.m = make([]int, maxIndex)
}

func (p *pendingRequests) AssertEmpty() {
	for _, count := range p.m {
		if count != 0 {
			panic(count)
		}
	}
}

func (p *pendingRequests) Get(r RequestIndex) int {
	return p.m[r]
}
