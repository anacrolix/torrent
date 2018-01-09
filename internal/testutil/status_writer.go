package testutil

import (
	"fmt"
	"io"
	"net/http"

	"github.com/anacrolix/missinggo"
)

type StatusWriter interface {
	WriteStatus(io.Writer)
}

func ExportStatusWriter(sw StatusWriter, path string) {
	http.HandleFunc(
		fmt.Sprintf("/%s/%s", missinggo.GetTestName(), path),
		func(w http.ResponseWriter, r *http.Request) {
			sw.WriteStatus(w)
		},
	)
}
