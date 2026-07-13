package app

import (
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/mod/semver"

	distrkeeper "github.com/sei-protocol/sei-chain/sei-cosmos/x/distribution/keeper"
)

// TestReadOnlyRewardsUpgradeGateReconciled guards the invariant that the
// reward-query upgrade gate constant (distrkeeper.ReadOnlyRewardsUpgrade) exactly
// matches the registered upgrade tag for its release.
//
// The two gate paths use different matching: the live/sync path resolves activation
// via an exact-string done-marker lookup (UpgradeKeeper.GetDoneHeight), while the
// tracing path uses semver.Compare, which treats "v6.7" and "v6.7.0" as equal. If
// the gate tag were ever registered in app/tags in a form that is semver-equal to
// the constant but not byte-equal (e.g. "v6.7.0" vs "v6.7"), the live and tracing
// paths would disagree for blocks in that upgrade — a trace/replay determinism
// split. This fails loudly if that ever happens.
//
// It passes vacuously until the gate's tag is actually registered, and then only if
// the registered tag is byte-identical to the constant.
func TestReadOnlyRewardsUpgradeGateReconciled(t *testing.T) {
	content, err := f.ReadFile("tags")
	require.NoError(t, err)

	require.True(t, semver.IsValid(distrkeeper.ReadOnlyRewardsUpgrade),
		"gate constant %q must be a valid semver tag", distrkeeper.ReadOnlyRewardsUpgrade)

	for _, tag := range parseUpgradesList(string(content)) {
		if semver.Compare(tag, distrkeeper.ReadOnlyRewardsUpgrade) == 0 {
			require.Equal(t, distrkeeper.ReadOnlyRewardsUpgrade, tag,
				"reward-query gate constant %q must byte-match the registered upgrade tag %q; "+
					"the live gate matches by exact string while the tracing gate uses semver, "+
					"so a semver-equal-but-different tag would split trace vs live",
				distrkeeper.ReadOnlyRewardsUpgrade, tag)
		}
	}
}
