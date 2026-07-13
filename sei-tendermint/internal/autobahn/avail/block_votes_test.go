package avail

import (
	"testing"
	"time"

	"github.com/sei-protocol/sei-chain/sei-tendermint/autobahn/types"
	"github.com/sei-protocol/sei-chain/sei-tendermint/libs/utils"
	"github.com/stretchr/testify/require"
)

// TestReweightPreservesHeader verifies that after reweight empties the votes
// slice (because committee rotation removed all voters for a block hash), the
// block header stored at first-vote time is still accessible. This matters
// because headers() reads entry.header to build the parent-hash chain for
// fullCommitQC, and that call arrives after reweightForNextEpoch has run.
func TestReweightPreservesHeader(t *testing.T) {
	rng := utils.TestRng()

	// Epoch 0: key A only. Epoch 1: key B only (disjoint committees).
	// LaneQuorum for a 1-validator committee is 1, so A's vote alone reaches quorum.
	// After reweight to ep1, A's vote is cleared from votes — but header must survive.
	keyA := types.GenSecretKey(rng)
	keyB := types.GenSecretKey(rng)

	makeEpoch := func(idx types.EpochIndex, weights map[types.PublicKey]uint64) *types.Epoch {
		c := utils.OrPanic1(types.NewCommittee(weights))
		first := types.RoadIndex(uint64(idx) * 108_000)
		rr := types.RoadRange{First: first, Last: first + 107_999}
		return types.NewEpoch(idx, rr, time.Time{}, c, 0)
	}

	ep0 := makeEpoch(0, map[types.PublicKey]uint64{keyA.Public(): 1})
	ep1 := makeEpoch(1, map[types.PublicKey]uint64{keyB.Public(): 1})

	lane := keyA.Public()
	block := types.NewBlock(lane, 0, types.BlockHeaderHash{}, types.GenPayload(rng))
	header := block.Header()

	bv := newBlockVotes()

	// A's vote reaches quorum immediately (LaneQuorum=1).
	lqc, ok := bv.pushVote(ep0, types.Sign(keyA, types.NewLaneVote(header)))
	require.NotNil(t, lqc, "quorum should be reached on A's vote")
	require.True(t, ok)

	h := header.Hash()
	entry := bv.byHash[h]
	require.NotNil(t, entry.header, "header should be set after pushVote")
	require.Equal(t, header, entry.header)
	require.Len(t, entry.votes, 1)

	// Reweight to epoch 1: keyA and keyB have weight 0, so votes is emptied.
	bv.reweight(ep1)

	entry = bv.byHash[h]
	require.Empty(t, entry.votes, "votes should be empty after reweight removes old-committee voters")
	require.Equal(t, uint64(0), entry.weight)
	require.NotNil(t, entry.header, "header must survive reweight")
	require.Equal(t, header, entry.header, "header content must be unchanged after reweight")
}
