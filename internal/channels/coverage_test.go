package channels

import (
	"errors"
	"fmt"
	"testing"
)

func TestCoverageClassifiesHistoryWithoutTransportDiagnostics(t *testing.T) {
	if InitialCoverage(Mattermost) != CoverageSnapshots || InitialCoverage(Slack) != CoverageSnapshots || InitialCoverage(IRC) != CoverageLiveConnection || InitialCoverage(Provider("unknown")) != "" {
		t.Fatal("wrong provider coverage")
	}
	if !errors.Is(fmt.Errorf("bounded poll: %w", ErrHistoryGap), ErrHistoryGap) {
		t.Fatal("typed history coverage signal cannot be classified")
	}
}
