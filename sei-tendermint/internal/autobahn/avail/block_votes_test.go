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

func TestLaneVoteSet_Add(t *testing.T) {
	rng := utils.TestRng()
	lane := types.GenSecretKey(rng).Public()
	header := types.NewBlock(lane, 0, types.BlockHeaderHash{}, types.GenPayload(rng)).Header()
	mkVote := func() *types.Signed[*types.LaneVote] {
		return types.Sign(types.GenSecretKey(rng), types.NewLaneVote(header))
	}

	set := &laneVoteSet{}
	require.False(t, set.add(1, 2, mkVote()).IsPresent())
	require.Equal(t, uint64(1), set.weight)
	require.Len(t, set.votes, 1)
	require.Nil(t, set.qc)

	qc, ok := set.add(1, 2, mkVote()).Get()
	require.True(t, ok)
	require.Equal(t, set.qc, qc)
	require.Equal(t, uint64(2), set.weight)
	require.Len(t, set.votes, 2)

	require.False(t, set.add(1, 2, mkVote()).IsPresent())
	require.Equal(t, uint64(2), set.weight)
	require.Len(t, set.votes, 2)

	heavy := &laneVoteSet{}
	require.True(t, heavy.add(3, 2, mkVote()).IsPresent())
	require.Equal(t, uint64(3), heavy.weight)
	require.Len(t, heavy.votes, 1)
	require.NotNil(t, heavy.qc)
}

func TestPushVote_ZeroWeightLeavesNoByHashEntry(t *testing.T) {
	rng := utils.TestRng()
	keyA := types.GenSecretKey(rng)
	keyZ := types.GenSecretKey(rng) // in neither epoch

	ep0 := makeVoteEpoch(0, map[types.PublicKey]uint64{keyA.Public(): 1})
	ep1 := makeVoteEpoch(1, map[types.PublicKey]uint64{keyA.Public(): 1})

	lane := keyA.Public()
	header := types.NewBlock(lane, 0, types.BlockHeaderHash{}, types.GenPayload(rng)).Header()

	bv := newBlockVotes()
	require.False(t, bv.pushVote([]*types.Epoch{ep0, ep1}, types.Sign(keyZ, types.NewLaneVote(header))).IsPresent())
	require.Contains(t, bv.byKey, keyZ.Public(), "vote retained in byKey for later back-fill")
	require.NotContains(t, bv.byHash, header.Hash(), "zero-weight vote must not create a byHash entry")
}

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

	// E reaches Next quorum only: stored on set, but return is None (Current-only).
	require.False(t, bv.pushVote(eps, types.Sign(keyE, types.NewLaneVote(header))).IsPresent(),
		"Next-only quorum must not return a QC")
	require.Contains(t, bv.byKey, keyE.Public())

	byEpoch := bv.byHash[h]
	_, inEp0 := byEpoch[0]
	require.False(t, inEp0, "E has no weight in ep0; the ep0 set must not be created")
	set1 := byEpoch[1]
	require.NotNil(t, set1)
	require.NotNil(t, set1.qc, "Next still stores its LaneQC")
	require.Len(t, set1.votes, 1)
	require.Equal(t, keyE.Public(), set1.votes[0].Key())
	require.Equal(t, header, set1.votes[0].Msg().Header())

	// A reaches Current quorum ⇒ returned for notify.
	currentQC, ok := bv.pushVote(eps, types.Sign(keyA, types.NewLaneVote(header))).Get()
	require.True(t, ok)
	set0 := byEpoch[0]
	require.NotNil(t, set0)
	require.Equal(t, currentQC, set0.qc)
	require.Len(t, set0.votes, 1)
	require.Equal(t, keyA.Public(), set0.votes[0].Key())
	require.Len(t, set1.votes, 1, "A's vote does not leak into the ep1 set")

	qc, ok := bv.laneQC(ep0).Get()
	require.True(t, ok)
	require.Equal(t, currentQC, qc)
	qc, ok = bv.laneQC(ep1).Get()
	require.True(t, ok)
	require.Equal(t, set1.qc, qc)
}

func TestApplyEpoch_BackfillsFromStoredVotes(t *testing.T) {
	rng := utils.TestRng()
	keyA := types.GenSecretKey(rng)
	keyB := types.GenSecretKey(rng)
	keyC := types.GenSecretKey(rng)
	keyD := types.GenSecretKey(rng)
	weights := map[types.PublicKey]uint64{
		keyA.Public(): 1, keyB.Public(): 1, keyC.Public(): 1, keyD.Public(): 1,
	}
	ep0 := makeVoteEpoch(0, weights)
	ep1 := makeVoteEpoch(1, weights)
	ep2 := makeVoteEpoch(2, weights)

	lane := keyA.Public()
	block := types.NewBlock(lane, 0, types.BlockHeaderHash{}, types.GenPayload(rng))
	header := block.Header()
	h := header.Hash()

	bv := newBlockVotes()
	bv.pushVote([]*types.Epoch{ep0, ep1}, types.Sign(keyA, types.NewLaneVote(header)))
	_, hasEp2 := bv.byHash[h][2]
	require.False(t, hasEp2, "ep2 not credited at push time")

	bv.applyEpoch(ep2)
	set2 := bv.byHash[h][2]
	require.NotNil(t, set2, "ep2 set seeded on entry")
	require.Len(t, set2.votes, 1)
	require.Equal(t, keyA.Public(), set2.votes[0].Key())
	require.Equal(t, uint64(1), set2.weight)
	require.Nil(t, set2.qc, "quorum is 2; backfill of one vote must not form a QC")

	bv.pushVote([]*types.Epoch{ep1, ep2}, types.Sign(keyB, types.NewLaneVote(header)))
	require.Len(t, set2.votes, 2, "B added to ep2 after entry")
	require.NotNil(t, set2.qc)
}

func TestPushVote_DedupsSigner(t *testing.T) {
	rng := utils.TestRng()
	keyA := types.GenSecretKey(rng)
	keyB := types.GenSecretKey(rng)
	keyC := types.GenSecretKey(rng)
	keyD := types.GenSecretKey(rng)
	weights := map[types.PublicKey]uint64{
		keyA.Public(): 1, keyB.Public(): 1, keyC.Public(): 1, keyD.Public(): 1,
	}
	eps := []*types.Epoch{makeVoteEpoch(0, weights), makeVoteEpoch(1, weights)}

	lane := keyA.Public()
	block := types.NewBlock(lane, 0, types.BlockHeaderHash{}, types.GenPayload(rng))
	header := block.Header()

	bv := newBlockVotes()
	vote := types.Sign(keyA, types.NewLaneVote(header))

	require.False(t, bv.pushVote(eps, vote).IsPresent(), "one of four validators is below quorum (2)")
	set := bv.byHash[header.Hash()][0]
	require.Equal(t, uint64(1), set.weight)

	require.False(t, bv.pushVote(eps, vote).IsPresent())
	require.Equal(t, uint64(1), set.weight, "duplicate vote must not double-count")
	require.Len(t, set.votes, 1)
}
