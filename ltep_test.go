package torrent_test

import (
	"math/rand"
	"strconv"
	"testing"

	"github.com/anacrolix/sync"
	qt "github.com/go-quicktest/qt"

	. "github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/internal/testutil"
	pp "github.com/anacrolix/torrent/peer_protocol"
)

const (
	testRepliesToOddsExtensionName  = "pm_me_odds"
	testRepliesToEvensExtensionName = "pm_me_evens"
)

func countHandler(
	t *testing.T,
	wg *sync.WaitGroup,
	// Name of the endpoint that this handler is for, for logging.
	handlerName string,
	// Whether we expect evens or odds
	expectedMod2 uint,
	// Extension name of messages we expect to handle.
	answerToName pp.ExtensionName,
	// Extension name of messages we expect to send.
	replyToName pp.ExtensionName,
	// Signal done when this value is seen.
	doneValue uint,
) func(event PeerConnReadExtensionMessageEvent) {
	return func(event PeerConnReadExtensionMessageEvent) {
		// Read handshake, don't look it up.
		if event.ExtensionNumber == 0 {
			return
		}
		name, builtin, err := event.PeerConn.LocalLtepProtocolMap.LookupId(event.ExtensionNumber)
		qt.Assert(t, qt.IsNil(err))
		// Not a user protocol.
		if builtin {
			return
		}
		switch name {
		case answerToName:
			u64, err := strconv.ParseUint(string(event.Payload), 10, 0)
			qt.Assert(t, qt.IsNil(err))
			i := uint(u64)
			t.Logf("%v got %d", handlerName, i)
			if i == doneValue {
				wg.Done()
				return
			}
			qt.Assert(t, qt.Equals(i%2, expectedMod2))
			go func() {
				qt.Assert(t, qt.IsNil(
					event.PeerConn.WriteExtendedMessage(
						replyToName,
						[]byte(strconv.FormatUint(uint64(i+1), 10)))))
			}()
		default:
			t.Fatalf("got unexpected extension name %q", name)
		}
	}
}

func TestUserLtep(t *testing.T) {
	var wg sync.WaitGroup

	makeCfg := func() *ClientConfig {
		cfg := TestingConfig(t)
		// Only want a single connection to between the clients.
		cfg.DisableUTP = true
		cfg.DisableIPv6 = true
		return cfg
	}

	evensCfg := makeCfg()
	evensCfg.Callbacks.ReadExtendedHandshake = func(pc *PeerConn, msg *pp.ExtendedHandshakeMessage) {
		// The client lock is held while handling this event, so we have to do synchronous work in a
		// separate goroutine.
		go func() {
			// Check sending an extended message for a protocol the peer doesn't support is an error.
			qt.Check(t, qt.IsNotNil(pc.WriteExtendedMessage("pm_me_floats", []byte("3.142"))))
			// Kick things off by sending a 1.
			qt.Check(t, qt.IsNil(pc.WriteExtendedMessage(testRepliesToOddsExtensionName, []byte("1"))))
		}()
	}
	evensCfg.Callbacks.PeerConnReadExtensionMessage = append(
		evensCfg.Callbacks.PeerConnReadExtensionMessage,
		countHandler(t, &wg, "evens", 0, testRepliesToEvensExtensionName, testRepliesToOddsExtensionName, 100))
	evensCfg.Callbacks.PeerConnAdded = append(evensCfg.Callbacks.PeerConnAdded, func(conn *PeerConn) {
		conn.LocalLtepProtocolMap.AddUserProtocol(testRepliesToEvensExtensionName)
		qt.Assert(t, qt.HasLen(conn.LocalLtepProtocolMap.Index[conn.LocalLtepProtocolMap.NumBuiltin:], 1))
	})

	oddsCfg := makeCfg()
	oddsCfg.Callbacks.PeerConnAdded = append(oddsCfg.Callbacks.PeerConnAdded, func(conn *PeerConn) {
		conn.LocalLtepProtocolMap.AddUserProtocol(testRepliesToOddsExtensionName)
		qt.Assert(t, qt.HasLen(conn.LocalLtepProtocolMap.Index[conn.LocalLtepProtocolMap.NumBuiltin:], 1))
	})
	oddsCfg.Callbacks.PeerConnReadExtensionMessage = append(
		oddsCfg.Callbacks.PeerConnReadExtensionMessage,
		countHandler(t, &wg, "odds", 1, testRepliesToOddsExtensionName, testRepliesToEvensExtensionName, 100))

	cl1, err := NewClient(oddsCfg)
	qt.Assert(t, qt.IsNil(err))
	defer cl1.Close()
	cl2, err := NewClient(evensCfg)
	qt.Assert(t, qt.IsNil(err))
	defer cl2.Close()
	addOpts := AddTorrentOpts{}
	rand.Read(addOpts.InfoHash[:])
	t1, _ := cl1.AddTorrentOpt(addOpts)
	t2, _ := cl2.AddTorrentOpt(addOpts)
	defer testutil.ExportStatusWriter(cl1, "cl1", t)()
	defer testutil.ExportStatusWriter(cl2, "cl2", t)()
	// Expect one PeerConn to see the value.
	wg.Add(1)
	added := t1.AddClientPeer(cl2)
	// Ensure some addresses for the other client were added.
	qt.Assert(t, qt.Not(qt.Equals(added, 0)))
	wg.Wait()
	_ = t2
}
