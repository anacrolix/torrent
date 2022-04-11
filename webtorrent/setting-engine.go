// These build constraints are copied from webrtc's settingengine.go.
//go:build !js
// +build !js

package webtorrent

import (
	"io"

	"github.com/pion/logging"
	"github.com/pion/webrtc/v3"
)

var s = webrtc.SettingEngine{
	// This could probably be done with better integration into anacrolix/log, but I'm not sure if
	// it's worth the effort.
	LoggerFactory: discardLoggerFactory{},
}

type discardLoggerFactory struct{}

func (discardLoggerFactory) NewLogger(scope string) logging.LeveledLogger {
	return logging.NewDefaultLeveledLoggerForScope(scope, logging.LogLevelInfo, io.Discard)
}
