package tmproot

import (
	"io/ioutil"
	"os"
	"sync"
)

type Dir struct {
	mu     sync.Mutex
	path   string
	inited bool
}

func (me *Dir) init(prefix string) bool {
	if me.inited {
		return false
	}
	var err error
	me.path, err = ioutil.TempDir("", prefix)
	if err != nil {
		panic(err)
	}
	me.inited = true
	return true
}

func (me *Dir) Init(prefix string) {
	me.mu.Lock()
	defer me.mu.Unlock()
	if me.inited {
		panic("already inited")
	}
	me.init(prefix)
}

func (me *Dir) lazyDefaultInit() {
	me.mu.Lock()
	defer me.mu.Unlock()
	me.init("")

}

func (me *Dir) NewSub() string {
	me.lazyDefaultInit()
	ret, err := ioutil.TempDir(me.path, "")
	if err != nil {
		panic(err)
	}
	return ret
}

func (me *Dir) RemoveAll() error {
	me.lazyDefaultInit()
	return os.RemoveAll(me.path)
}
