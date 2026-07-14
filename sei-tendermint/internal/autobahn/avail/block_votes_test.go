package avail

import (
	"testing"
	"time"

	"github.com/sei-protocol/sei-chain/sei-tendermint/autobahn/types"
	"github.com/sei-protocol/sei-chain/sei-tendermint/libs/utils"
	"github.com/stretchr/testify/require"
)

func makeVoteEpoch(idx types.EpochIndex, weights map[types.PublicKey]uint64) *types.Epoch {
	c := utils.OrPanic1(types.NewCommittee(weights))
	first := types.RoadIndex(uint64(idx) * 108_000)
	rr := types.RoadRange{First: first, Last: first + 107_999}
	return types.NewEpoch(idx, rr, time.Time{}, c, 0)
}

// TestLaneVoteSet_Add exercises the weight accumulation and quorum edge of the
// laneVoteSet primitive in isolation: it accumulates until quorum, reports the
// crossing exactly once, and is a no-op afterwards.
func TestLaneVoteSet_Add(t *testing.T) {
	rng := utils.TestRng()
	lane := types.GenSecretKey(rng).Public()
	header := types.NewBlock(lane, 0, types.BlockHeaderHash{}, types.GenPayload(rng)).Header()
	mkVote := func() *types.Signed[*types.LaneVote] {
		return types.Sign(types.GenSecretKey(rng), types.NewLaneVote(header))
	}

	set := &laneVoteSet{}
	// Below quorum (2): accumulates, returns false.
	require.False(t, set.add(1, 2, mkVote()))
	require.Equal(t, uint64(1), set.weight)
	require.Len(t, set.votes, 1)
	// Crosses quorum: returns true exactly on the crossing.
	require.True(t, set.add(1, 2, mkVote()))
	require.Equal(t, uint64(2), set.weight)
	require.Len(t, set.votes, 2)
	// Already at quorum: no-op, returns false, does not append.
	require.False(t, set.add(1, 2, mkVote()))
	require.Equal(t, uint64(2), set.weight)
	require.Len(t, set.votes, 2)

	// A single heavy vote can cross quorum from empty in one step.
	heavy := &laneVoteSet{}
	require.True(t, heavy.add(3, 2, mkVote()))
	require.Equal(t, uint64(3), heavy.weight)
	require.Len(t, heavy.votes, 1)
}

// TestPushVote_ZeroWeightLeavesNoByHashEntry verifies the lazy-creation
// invariant: a vote whose signer has no weight in any credited epoch is kept in
// byKey (for later back-fill) but creates no byHash entry, so headers() never
// observes an empty per-epoch map.
func TestPushVote_ZeroWeightLeavesNoByHashEntry(t *testing.T) {
	rng := utils.TestRng()
	keyA := types.GenSecretKey(rng)
	keyZ := types.GenSecretKey(rng) // in neither epoch

	ep0 := makeVoteEpoch(0, map[types.PublicKey]uint64{keyA.Public(): 1})
	ep1 := makeVoteEpoch(1, map[types.PublicKey]uint64{keyA.Public(): 1})

	lane := keyA.Public()
	header := types.NewBlock(lane, 0, types.BlockHeaderHash{}, types.GenPayload(rng)).Header()

	bv := newBlockVotes()
	require.False(t, bv.pushVote([]*types.Epoch{ep0, ep1}, types.Sign(keyZ, types.NewLaneVote(header))))
	require.Contains(t, bv.byKey, keyZ.Public(), "vote retained in byKey for later back-fill")
	require.NotContains(t, bv.byHash, header.Hash(), "zero-weight vote must not create a byHash entry")
}

// TestPushVote_AccumulatesPerEpoch verifies that a vote is credited only to the
// epochs under which its signer has weight, each tracked in its own laneVoteSet.
// A next-epoch-only signer contributes to the next epoch's set but leaves the
// current epoch's set untouched, so no reweight is needed at the boundary.
func TestPushVote_AccumulatesPerEpoch(t *testing.T) {
	rng := utils.TestRng()

	keyA := types.GenSecretKey(rng) // current epoch only
	keyE := types.GenSecretKey(rng) // next epoch only

	ep0 := makeVoteEpoch(0, map[types.PublicKey]uint64{keyA.Public(): 1})
	ep1 := makeVoteEpoch(1, map[types.PublicKey]uint64{keyE.Public(): 1})
	eps := []*types.Epoch{ep0, ep1}

	lane := keyA.Public()
	block := types.NewBlock(lane, 0, types.BlockHeaderHash{}, types.GenPayload(rng))
	header := block.Header()
	h := header.Hash()

	bv := newBlockVotes()

	// E votes first: weight 0 in ep0, 1 in ep1 (ep1 LaneQuorum == 1). E reaches
	// the next epoch's quorum but NOT the current epoch's, so pushVote reports
	// nothing — WaitForLaneQCs only cares about the current epoch.
	require.False(t, bv.pushVote(eps, types.Sign(keyE, types.NewLaneVote(header))),
		"a next-epoch-only quorum must not notify")
	require.Contains(t, bv.byKey, keyE.Public())

	byEpoch := bv.byHash[h]
	_, inEp0 := byEpoch[0]
	require.False(t, inEp0, "E has no weight in ep0; the ep0 set must not be created")
	set1 := byEpoch[1]
	require.NotNil(t, set1)
	require.Len(t, set1.votes, 1, "E's vote is credited to ep1 only")
	require.Equal(t, keyE.Public(), set1.votes[0].Key())
	// The block header is recoverable from any set's votes[0] (no dedicated field).
	require.Equal(t, header, set1.votes[0].Msg().Header())

	// A votes: weight 1 in ep0, 0 in ep1. Reaches the ep0 quorum → notifies.
	require.True(t, bv.pushVote(eps, types.Sign(keyA, types.NewLaneVote(header))))
	set0 := byEpoch[0]
	require.NotNil(t, set0)
	require.Len(t, set0.votes, 1, "only A's vote is in the ep0 set")
	require.Equal(t, keyA.Public(), set0.votes[0].Key())
	require.Len(t, set1.votes, 1, "A's vote does not leak into the ep1 set")
}

// TestApplyEpoch_BackfillsFromStoredVotes verifies that when an epoch newly
// enters the window, its set is seeded from votes that arrived before it was in
// range. This covers a block voted while Current=N but finalized under N+2 with
// an identical committee: without back-fill its N+2 set would be empty.
func TestApplyEpoch_BackfillsFromStoredVotes(t *testing.T) {
	rng := utils.TestRng()
	keyA := types.GenSecretKey(rng)
	keyB := types.GenSecretKey(rng)
	keyC := types.GenSecretKey(rng)
	keyD := types.GenSecretKey(rng)
	weights := map[types.PublicKey]uint64{
		keyA.Public(): 1, keyB.Public(): 1, keyC.Public(): 1, keyD.Public(): 1,
	}
	// ep0=current, ep1=next at push time; ep2 shares the same committee.
	ep0 := makeVoteEpoch(0, weights)
	ep1 := makeVoteEpoch(1, weights)
	ep2 := makeVoteEpoch(2, weights)

	lane := keyA.Public()
	block := types.NewBlock(lane, 0, types.BlockHeaderHash{}, types.GenPayload(rng))
	header := block.Header()
	h := header.Hash()

	bv := newBlockVotes()
	// A votes while the window is {ep0, ep1}: credited to ep0 and ep1 only.
	bv.pushVote([]*types.Epoch{ep0, ep1}, types.Sign(keyA, types.NewLaneVote(header)))
	_, hasEp2 := bv.byHash[h][2]
	require.False(t, hasEp2, "ep2 not credited at push time")

	// ep2 enters the window as Next → back-fill from stored votes.
	bv.applyEpoch(ep2)
	set2 := bv.byHash[h][2]
	require.NotNil(t, set2, "ep2 set seeded on entry")
	require.Len(t, set2.votes, 1)
	require.Equal(t, keyA.Public(), set2.votes[0].Key())
	require.Equal(t, uint64(1), set2.weight)

	// Idempotency guard within a single entry: a later signer arriving via
	// pushVote (window now {ep1, ep2}) adds to ep2 without disturbing A's credit.
	bv.pushVote([]*types.Epoch{ep1, ep2}, types.Sign(keyB, types.NewLaneVote(header)))
	require.Len(t, set2.votes, 2, "B added to ep2 after entry")
}

// TestPushVote_DedupsSigner verifies a signer's second vote for the same block
// is ignored (byKey dedup), so weight is never double-counted.
func TestPushVote_DedupsSigner(t *testing.T) {
	rng := utils.TestRng()
	keyA := types.GenSecretKey(rng)
	// Four validators (LaneQuorum = Faulty()+1 = 2) so a single vote is below
	// quorum and the effect of a duplicate is observable.
	keyB := types.GenSecretKey(rng)
	keyC := types.GenSecretKey(rng)
	keyD := types.GenSecretKey(rng)
	weights := map[types.PublicKey]uint64{
		keyA.Public(): 1, keyB.Public(): 1, keyC.Public(): 1, keyD.Public(): 1,
	}
	// Distinct current/next epochs (as in production) with the same committee.
	eps := []*types.Epoch{makeVoteEpoch(0, weights), makeVoteEpoch(1, weights)}

	lane := keyA.Public()
	block := types.NewBlock(lane, 0, types.BlockHeaderHash{}, types.GenPayload(rng))
	header := block.Header()

	bv := newBlockVotes()
	vote := types.Sign(keyA, types.NewLaneVote(header))

	require.False(t, bv.pushVote(eps, vote), "one of four validators is below quorum (2)")
	set := bv.byHash[header.Hash()][0]
	require.Equal(t, uint64(1), set.weight)

	// Re-pushing A's vote must not add weight again.
	require.False(t, bv.pushVote(eps, vote))
	require.Equal(t, uint64(1), set.weight, "duplicate vote must not double-count")
	require.Len(t, set.votes, 1)
}
