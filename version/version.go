// Package version provides default versions, user-agents etc. for client identification.
package version

import (
	"fmt"
	"reflect"
	"runtime/debug"
	"strings"
)

var (
	DefaultExtendedHandshakeClientVersion string
	// This should be updated when client behaviour changes in a way that other peers could care
	// about.
	DefaultBep20Prefix   = "-GT0003-"
	DefaultHttpUserAgent string
	DefaultUpnpId        string

	// libtorrent/src/http_tracker_connection.cpp
	AnonymousHttpUserAgent = "curl/7.81.0"

	// ExtendedHandshakeMessage struct: V string `bencode:"v,omitempty"`
	// so the "v" field is omitted when empty
	AnonymousExtendedHandshakeClientVersion = ""

	AnonymousBep20Prefix string

	// libtorrent/src/upnp.cpp
	// NewPortMappingDescription is empty
	AnonymousUpnpId = ""
)

func init() {
	const (
		longNamespace   = "anacrolix"
		longPackageName = "torrent"
	)
	type Newtype struct{}
	var newtype Newtype
	thisPkg := reflect.TypeOf(newtype).PkgPath()
	var (
		mainPath       = "unknown"
		mainVersion    = "unknown"
		torrentVersion = "unknown"
	)
	if buildInfo, ok := debug.ReadBuildInfo(); ok {
		mainPath = buildInfo.Main.Path
		mainVersion = buildInfo.Main.Version
		thisModule := ""
		// Note that if the main module is the same as this module, we get a version of "(devel)".
		for _, dep := range append(buildInfo.Deps, &buildInfo.Main) {
			if strings.HasPrefix(thisPkg, dep.Path) && len(dep.Path) >= len(thisModule) {
				thisModule = dep.Path
				torrentVersion = dep.Version
			}
		}
	}
	DefaultExtendedHandshakeClientVersion = fmt.Sprintf(
		"%v %v (%v/%v %v)",
		mainPath,
		mainVersion,
		longNamespace,
		longPackageName,
		torrentVersion,
	)
	DefaultUpnpId = fmt.Sprintf("%v %v", mainPath, mainVersion)
	// Per https://developer.mozilla.org/en-US/docs/Web/HTTP/Headers/User-Agent#library_and_net_tool_ua_strings
	DefaultHttpUserAgent = fmt.Sprintf(
		"%v-%v/%v",
		longNamespace,
		longPackageName,
		torrentVersion,
	)
	// libtorrent/bindings/c/library.cpp
	// fingerprint fing("LT", lt::version_major, lt::version_minor, lt::version_tiny, 0);
	// libtorrent 2.0.11 = 2025-01-28
	AnonymousBep20Prefix = GenerateFingerprint("LT", 2, 0, 11, 0)
}
