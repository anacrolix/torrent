package torrent

import "time"

// stealRequestGraceEnvKey overrides ClientConfig.StealRequestGrace for callers that build
// their config with NewDefaultClientConfig and expose no knob of their own. It takes a
// time.ParseDuration value ("0", "25ms", "1s"), and exists so the grace can be swept across
// runs of a downstream client without rebuilding it.
const stealRequestGraceEnvKey = "TORRENT_STEAL_REQUEST_GRACE"

// defaultStealRequestGrace is the grace applied when neither the caller nor the environment
// asks for one.
//
// Zero, so that a client built from NewDefaultClientConfig behaves exactly as it did before
// the grace existed. The value that should be shipped is the one an experiment picks; until
// there is one, defaulting to any other number would change every downstream's request
// behaviour on the strength of an argument rather than a measurement.
const defaultStealRequestGrace time.Duration = 0
