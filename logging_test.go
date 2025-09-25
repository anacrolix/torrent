package torrent

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"math"
	"strings"
	"testing"

	"github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/tracker/shared"
)

type testHandler struct{}

func TestLazyLogValuer(t *testing.T) {
	// This is a test to ensure that the lazyLogValuer type implements the slog.LogValuer interface.
	// The test is intentionally left empty because the implementation is already verified by the
	// compiler.

	//h := testHandler{}
	//l := slog.New(h)
	var buf bytes.Buffer
	h := slog.NewTextHandler(&buf, &slog.HandlerOptions{
		Level: slog.Level(math.MinInt),
	})
	l := slog.New(h)
	cl := newTestingClient(t)
	tor := cl.newTorrentForTesting()
	l2 := tor.withSlogger(l)
	l2.Debug("hi")
	t.Log(buf.String())
}

func TestAnnounceEventJSONHandlerLogsAsString(t *testing.T) {
	// Test that when logging a struct containing an AnnounceEvent using a JSONHandler,
	// the AnnounceEvent is serialized as a string and not an integer.

	type TestStruct struct {
		Event shared.AnnounceEvent `json:"event"`
	}

	var buf bytes.Buffer
	h := slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		Level: slog.Level(math.MinInt),
	})
	l := slog.New(h)

	testData := TestStruct{
		Event: shared.Started,
	}

	l.Info("test log entry", "data", testData)

	logOutput := buf.String()
	t.Logf("Log output: %s", logOutput)

	// Parse the JSON to verify the event is logged as a string
	var logEntry map[string]interface{}
	err := json.Unmarshal([]byte(logOutput), &logEntry)
	qt.Assert(t, qt.IsNil(err))

	// Extract the data field
	data, ok := logEntry["data"].(map[string]interface{})
	qt.Assert(t, qt.IsTrue(ok), qt.Commentf("Expected 'data' field to be an object, got %T", logEntry["data"]))

	// Check that the event field is a string, not a number
	event, ok := data["event"].(string)
	qt.Assert(t, qt.IsTrue(ok), qt.Commentf("Expected 'event' field to be a string, got %T with value %v", data["event"], data["event"]))

	// Verify the string value matches the expected string representation
	expectedEventString := shared.Started.String()
	qt.Assert(t, qt.Equals(event, expectedEventString))

	// Verify it contains the expected string representation
	expectedJSON := `"event":"started"`
	qt.Assert(t, qt.IsTrue(strings.Contains(logOutput, expectedJSON)), qt.Commentf("Expected log output to contain %s, but it didn't. Full output: %s", expectedJSON, logOutput))

	// Additional verification: ensure it's not serialized as an integer
	qt.Assert(t, qt.IsFalse(strings.Contains(logOutput, `"event":2`)), qt.Commentf("Event was serialized as integer (2) instead of string"))
	qt.Assert(t, qt.IsFalse(strings.Contains(logOutput, `"event": 2`)), qt.Commentf("Event was serialized as integer (2) instead of string"))
}

func TestSlogLoggerWithSameNameCreatesDuplicateGroups(t *testing.T) {
	var buf bytes.Buffer
	h := slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		Level: slog.Level(math.MinInt),
	})
	l := slog.New(h)

	// Create logger with initial group
	l1 := l.With(slog.Group("test", "key1", "value1", "key2", "value2"))

	// Add another group with same name - this creates duplicate entries
	l2 := l1.With(slog.Group("test", "key3", "value3"))

	l2.Info("test message")

	logOutput := buf.String()
	t.Logf("Log output: %s", logOutput)

	// Verify both groups appear in the output (slog doesn't replace, it creates duplicates)
	qt.Assert(t, qt.IsTrue(strings.Contains(logOutput, `"test":{"key1":"value1","key2":"value2"}`)))
	qt.Assert(t, qt.IsTrue(strings.Contains(logOutput, `"test":{"key3":"value3"}`)))

	// When parsing JSON with duplicate keys, the last one wins
	var logEntry map[string]interface{}
	err := json.Unmarshal([]byte(logOutput), &logEntry)
	qt.Assert(t, qt.IsNil(err))

	testGroup, ok := logEntry["test"].(map[string]interface{})
	qt.Assert(t, qt.IsTrue(ok), qt.Commentf("Expected 'test' field to be an object"))

	// The parsed JSON will have only the last occurrence due to JSON duplicate key handling
	qt.Assert(t, qt.Equals(testGroup["key3"], "value3"))
	_, hasKey1 := testGroup["key1"]
	qt.Assert(t, qt.IsFalse(hasKey1), qt.Commentf("Due to JSON duplicate key handling, only the last group should be present"))
}
