package avail

import (
	"github.com/sei-protocol/sei-chain/sei-tendermint/autobahn/types"
)

// laneVoteSet accumulates weighted votes for one block hash under a single
// epoch's committee. The epoch it belongs to is the key in blockVotes.byHash,
// so the epoch a weight is valid for is always explicit.
type laneVoteSet struct {
	weight uint64
	votes  []*types.Signed[*types.LaneVote]
}

// add records vote's weight and returns true iff the set newly reached quorum
// on this vote. It is a no-op (returns false) once quorum is already met.
func (s *laneVoteSet) add(weight, quorum uint64, vote *types.Signed[*types.LaneVote]) bool {
	if s.weight >= quorum {
		return false
	}
	s.weight += weight
	s.votes = append(s.votes, vote)
	return s.weight >= quorum
}

// blockVotes tracks votes for a single (lane, block number) across the epochs
// in the current trio window. Weight is accumulated per epoch independently,
// so advancing the epoch needs no reweight: the next epoch's laneVoteSet has
// been filled in parallel as votes arrived. A vote can only reach here after
// EpochTrio.VerifyInWindow accepts it against Current or Next, so accumulating
// into exactly those two epochs is complete.
type blockVotes struct {
	// byKey dedups votes (one per signer per block) and is the durable record
	// applyEpoch replays to back-fill an epoch entering the window. Entries are
	// never deleted individually — a signer weight-0 in the current window may
	// have weight in a later epoch — the whole map is freed when the block
	// number is pruned from the vote queue (inner.prune).
	byKey map[types.PublicKey]*types.Signed[*types.LaneVote]
	// byHash maps a block hash to its per-epoch vote sets. An entry exists only
	// once a weighted vote has been credited (credit creates it lazily), so a
	// present entry always has a non-empty set — headers() can recover the block
	// header from any set's votes[0]. A vote that earns no weight in any epoch it
	// is credited against (e.g. a signer valid only in a now-departed epoch)
	// leaves no byHash entry; it lives only in byKey.
	byHash map[types.BlockHeaderHash]map[types.EpochIndex]*laneVoteSet
}

func newBlockVotes() blockVotes {
	return blockVotes{
		byKey:  map[types.PublicKey]*types.Signed[*types.LaneVote]{},
		byHash: map[types.BlockHeaderHash]map[types.EpochIndex]*laneVoteSet{},
	}
}

// pushVote records vote and accumulates its weight into the laneVoteSet of
// every epoch in eps under which the signer has non-zero weight. Callers pass
// the current and next epochs; a signer present only in the next epoch counts
// toward the next epoch's set immediately, so no reweight is needed when the
// epoch advances.
//
// Returns true iff the current epoch (eps[0]) newly reached its lane quorum on
// this vote — the signal for the caller to wake WaitForLaneQCs, which reads the
// current epoch's set. A quorum that forms only in the next epoch's set is
// silent; it is picked up when the boundary advance wakes waiters.
func (bv blockVotes) pushVote(eps []*types.Epoch, vote *types.Signed[*types.LaneVote]) bool {
	k := vote.Key()
	if _, ok := bv.byKey[k]; ok {
		return false
	}
	bv.byKey[k] = vote

	notify := false
	for i, ep := range eps {
		// Notify only for the current epoch (eps[0]).
		if bv.credit(ep, vote) && i == 0 {
			notify = true
		}
	}
	return notify
}

// credit adds vote to ep's set for the vote's block hash and returns whether
// ep's set newly reached quorum. A no-op for signers with no weight in ep. The
// byHash entry (and its per-epoch set) is created lazily here, only when a
// weighted vote is actually credited — a vote with no weight in any epoch it is
// credited against leaves no byHash entry, so a present entry is never empty.
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

// applyEpoch credits every stored vote to ep's set. It is called when ep newly
// enters the trio window as the Next epoch: a vote is only ever credited to the
// Current and Next epochs at push time, so a block whose votes arrived before
// ep was in range — yet finalizes under ep (e.g. a lagging lane, or ep sharing
// the prior epoch's committee) — would otherwise have an empty set under ep and
// never reach quorum. ep's sets are empty beforehand (ep was neither Current
// nor Next until now), so back-filling from byKey does not double-count.
func (bv blockVotes) applyEpoch(ep *types.Epoch) {
	for _, vote := range bv.byKey {
		bv.credit(ep, vote)
	}
}
