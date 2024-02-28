package torrent_test

import (
	"math/rand"
	"strconv"
	"testing"

	"github.com/anacrolix/sync"
	qt "github.com/frankban/quicktest"

	. "github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/internal/testutil"
	pp "github.com/anacrolix/torrent/peer_protocol"
)

const (
	testRepliesToOddsExtensionName  = "pm_me_odds"
	testRepliesToEvensExtensionName = "pm_me_evens"
)

func countHandler(
	c *qt.C,
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
		c.Assert(err, qt.IsNil)
		// Not a user protocol.
		if builtin {
			return
		}
		switch name {
		case answerToName:
			u64, err := strconv.ParseUint(string(event.Payload), 10, 0)
			c.Assert(err, qt.IsNil)
			i := uint(u64)
			c.Logf("%v got %d", handlerName, i)
			if i == doneValue {
				wg.Done()
				return
			}
			c.Assert(i%2, qt.Equals, expectedMod2)
			go func() {
				c.Assert(
					event.PeerConn.WriteExtendedMessage(
						replyToName,
						[]byte(strconv.FormatUint(uint64(i+1), 10))),
					qt.IsNil)
			}()
		default:
			c.Fatalf("got unexpected extension name %q", name)
		}
	}
}

func TestUserLtep(t *testing.T) {
	c := qt.New(t)
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
			c.Check(pc.WriteExtendedMessage("pm_me_floats", []byte("3.142")), qt.IsNotNil)
			// Kick things off by sending a 1.
			c.Check(pc.WriteExtendedMessage(testRepliesToOddsExtensionName, []byte("1")), qt.IsNil)
		}()
	}
	evensCfg.Callbacks.PeerConnReadExtensionMessage = append(
		evensCfg.Callbacks.PeerConnReadExtensionMessage,
		countHandler(c, &wg, "evens", 0, testRepliesToEvensExtensionName, testRepliesToOddsExtensionName, 100))
	evensCfg.Callbacks.PeerConnAdded = append(evensCfg.Callbacks.PeerConnAdded, func(conn *PeerConn) {
		conn.LocalLtepProtocolMap.AddUserProtocol(testRepliesToEvensExtensionName)
		c.Assert(conn.LocalLtepProtocolMap.Index[conn.LocalLtepProtocolMap.NumBuiltin:], qt.HasLen, 1)
	})

	oddsCfg := makeCfg()
	oddsCfg.Callbacks.PeerConnAdded = append(oddsCfg.Callbacks.PeerConnAdded, func(conn *PeerConn) {
		conn.LocalLtepProtocolMap.AddUserProtocol(testRepliesToOddsExtensionName)
		c.Assert(conn.LocalLtepProtocolMap.Index[conn.LocalLtepProtocolMap.NumBuiltin:], qt.HasLen, 1)
	})
	oddsCfg.Callbacks.PeerConnReadExtensionMessage = append(
		oddsCfg.Callbacks.PeerConnReadExtensionMessage,
		countHandler(c, &wg, "odds", 1, testRepliesToOddsExtensionName, testRepliesToEvensExtensionName, 100))

	cl1, err := NewClient(oddsCfg)
	c.Assert(err, qt.IsNil)
	defer cl1.Close()
	cl2, err := NewClient(evensCfg)
	c.Assert(err, qt.IsNil)
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
	c.Assert(added, qt.Not(qt.Equals), 0)
	wg.Wait()
	_ = t2
}
