package avail

import (
	"github.com/sei-protocol/sei-chain/sei-tendermint/autobahn/types"
)

type blockVotes struct {
	byKey  map[types.PublicKey]*types.Signed[*types.LaneVote]
	byHash map[types.BlockHeaderHash]*voteSet[*types.Signed[*types.LaneVote]]
}

func newBlockVotes() blockVotes {
	return blockVotes{
		byKey:  map[types.PublicKey]*types.Signed[*types.LaneVote]{},
		byHash: map[types.BlockHeaderHash]*voteSet[*types.Signed[*types.LaneVote]]{},
	}
}

// pushVote may store the vote for the current and next Epoch, but only
// accumulates weight for currentEpoch.
// Returns true iff a new QC has been constructed.
func (bv blockVotes) pushVote(ep *types.Epoch, vote *types.Signed[*types.LaneVote]) (*types.LaneQC, bool) {
	c := ep.Committee()
	k := vote.Key()
	h := vote.Msg().Header().Hash()
	if _, ok := bv.byKey[k]; ok {
		return nil, false
	}
	bv.byKey[k] = vote
	byHash, ok := bv.byHash[h]
	if !ok {
		byHash = &voteSet[*types.Signed[*types.LaneVote]]{}
		bv.byHash[h] = byHash
	}
	if byHash.weight >= c.LaneQuorum() {
		return nil, false
	}
	byHash.weight += c.Weight(k)
	byHash.votes = append(byHash.votes, vote)
	if byHash.weight >= c.LaneQuorum() {
		return types.NewLaneQC(byHash.votes), true
	}
	return nil, false
}

// reweight recalculates weights and vote lists for all stored votes using
// newEpoch's committee. Called when the epoch advances so that votes from
// validators who were in the next epoch are now counted. Returns true if any
// block hash newly reached quorum under the new committee.
func (bv blockVotes) reweight(newEpoch *types.Epoch) bool {
	c := newEpoch.Committee()
	for _, set := range bv.byHash {
		set.weight = 0
		set.votes = set.votes[:0]
	}
	quorumReached := false
	for k, vote := range bv.byKey {
		w := c.Weight(k)
		if w == 0 {
			continue
		}
		h := vote.Msg().Header().Hash()
		set := bv.byHash[h]
		if set.weight >= c.LaneQuorum() {
			continue
		}
		set.weight += w
		set.votes = append(set.votes, vote)
		if set.weight >= c.LaneQuorum() {
			quorumReached = true
		}
	}
	return quorumReached
}
