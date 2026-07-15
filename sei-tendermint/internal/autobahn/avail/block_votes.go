package avail

import (
	"github.com/sei-protocol/sei-chain/sei-tendermint/autobahn/types"
)

// laneVoteSet is weighted votes for one (block hash, epoch).
type laneVoteSet struct {
	weight uint64
	votes  []*types.Signed[*types.LaneVote]
}

// add returns true iff this vote newly reaches quorum.
func (s *laneVoteSet) add(weight, quorum uint64, vote *types.Signed[*types.LaneVote]) bool {
	if s.weight >= quorum {
		return false
	}
	s.weight += weight
	s.votes = append(s.votes, vote)
	return s.weight >= quorum
}

// blockVotes is per-(lane, height) vote state. Weight is tracked per epoch, so
// an epoch advance does not reweight existing sets.
type blockVotes struct {
	// byKey: one vote per signer; source for applyEpoch backfill.
	byKey map[types.PublicKey]*types.Signed[*types.LaneVote]
	// byHash: per-hash, per-epoch weighted sets (lazy; present ⇒ non-empty).
	byHash map[types.BlockHeaderHash]map[types.EpochIndex]*laneVoteSet
}

func newBlockVotes() blockVotes {
	return blockVotes{
		byKey:  map[types.PublicKey]*types.Signed[*types.LaneVote]{},
		byHash: map[types.BlockHeaderHash]map[types.EpochIndex]*laneVoteSet{},
	}
}

// pushVote credits vote into each eps[i] where the signer has weight.
// Contract: eps[0] is Current, later entries are Next (and only those).
// Returns true iff Current newly hit lane quorum (wake WaitForLaneQCs).
// Next-only quorum is silent until the boundary advance wakes waiters.
func (bv blockVotes) pushVote(eps []*types.Epoch, vote *types.Signed[*types.LaneVote]) bool {
	k := vote.Key()
	if _, ok := bv.byKey[k]; ok {
		return false
	}
	bv.byKey[k] = vote

	notify := false
	for i, ep := range eps {
		if bv.credit(ep, vote) && i == 0 {
			notify = true
		}
	}
	return notify
}

// credit adds vote under ep; returns whether ep's set newly reached quorum.
func (bv blockVotes) credit(ep *types.Epoch, vote *types.Signed[*types.LaneVote]) bool {
	c := ep.Committee()
	w := c.Weight(vote.Key())
	if w == 0 {
		return false
	}
	h := vote.Msg().Header().Hash()
	byEpoch, ok := bv.byHash[h]
	if !ok {
		byEpoch = map[types.EpochIndex]*laneVoteSet{}
		bv.byHash[h] = byEpoch
	}
	set := byEpoch[ep.EpochIndex()]
	if set == nil {
		set = &laneVoteSet{}
		byEpoch[ep.EpochIndex()] = set
	}
	return set.add(w, c.LaneQuorum(), vote)
}

// applyEpoch backfills ep from byKey when ep newly enters the window as Next.
// ep's sets are empty beforehand, so this does not double-count.
func (bv blockVotes) applyEpoch(ep *types.Epoch) {
	for _, vote := range bv.byKey {
		bv.credit(ep, vote)
	}
}
