package dirwatch

import (
	"io/ioutil"
	"os"
	"testing"
)

func TestDirwatch(t *testing.T) {
	tempDirName, err := ioutil.TempDir("", "")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDirName)
	dw, err := New(tempDirName)
	if err != nil {
		t.Fatal(err)
	}
	dw.Close()
}
