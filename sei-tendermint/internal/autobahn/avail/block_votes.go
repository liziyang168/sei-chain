package avail

import (
	"github.com/sei-protocol/sei-chain/sei-tendermint/autobahn/types"
	"github.com/sei-protocol/sei-chain/sei-tendermint/libs/utils"
)

// laneVoteSet is weighted votes for one (block hash, epoch).
// qc is set once when weight first reaches quorum.
type laneVoteSet struct {
	weight uint64
	votes  []*types.Signed[*types.LaneVote]
	qc     *types.LaneQC
}

// add credits vote. Returns the newly formed LaneQC iff this vote crosses quorum.
func (s *laneVoteSet) add(weight, quorum uint64, vote *types.Signed[*types.LaneVote]) utils.Option[*types.LaneQC] {
	if s.qc != nil {
		return utils.None[*types.LaneQC]()
	}
	s.weight += weight
	s.votes = append(s.votes, vote)
	if s.weight < quorum {
		return utils.None[*types.LaneQC]()
	}
	s.qc = types.NewLaneQC(s.votes)
	return utils.Some(s.qc)
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
// Stores a LaneQC on any epoch that newly reaches quorum, but returns only the
// newly formed Current QC. Caller notifies when present.
func (bv blockVotes) pushVote(eps []*types.Epoch, vote *types.Signed[*types.LaneVote]) utils.Option[*types.LaneQC] {
	k := vote.Key()
	if _, ok := bv.byKey[k]; ok {
		return utils.None[*types.LaneQC]()
	}
	bv.byKey[k] = vote

	current := utils.None[*types.LaneQC]()
	for i, ep := range eps {
		formed := bv.credit(ep, vote)
		if i == 0 {
			current = formed
		}
	}
	return current
}

// credit adds vote under ep; returns a newly formed LaneQC if this vote crosses quorum.
func (bv blockVotes) credit(ep *types.Epoch, vote *types.Signed[*types.LaneVote]) utils.Option[*types.LaneQC] {
	c := ep.Committee()
	w := c.Weight(vote.Key())
	if w == 0 {
		return utils.None[*types.LaneQC]()
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

// laneQC returns a stored LaneQC for ep, if any hash has one.
func (bv blockVotes) laneQC(ep *types.Epoch) utils.Option[*types.LaneQC] {
	epIdx := ep.EpochIndex()
	for _, byEpoch := range bv.byHash {
		if set, ok := byEpoch[epIdx]; ok && set.qc != nil {
			return utils.Some(set.qc)
		}
	}
	return utils.None[*types.LaneQC]()
}
