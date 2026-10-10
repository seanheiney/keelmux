package channels

import "errors"

// ErrHistoryGap is a safe typed coverage signal, not a raw transport diagnostic.
// Missing history never authorizes a repeated external send.
var ErrHistoryGap = errors.New("channel history does not cover this cursor")

type Coverage string

const (
	CoverageSnapshots      Coverage = "since_activation_snapshots"
	CoverageLiveConnection Coverage = "live_connection_only"
	CoverageHistoryGap     Coverage = "history_gap"
)

func InitialCoverage(provider Provider) Coverage {
	switch provider {
	case Mattermost, Slack:
		return CoverageSnapshots
	case IRC:
		return CoverageLiveConnection
	default:
		return ""
	}
}
