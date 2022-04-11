// These build constraints are copied from webrtc's settingengine_js.go.
//go:build js && wasm
// +build js,wasm

package webtorrent

import (
	"github.com/pion/webrtc/v3"
)

// I'm not sure what to do for logging for JS. See
// https://gophers.slack.com/archives/CAK2124AG/p1649651943947579.
var s = webrtc.SettingEngine{}
