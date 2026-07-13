package avail

import (
	"fmt"
	"log/slog"

	"github.com/sei-protocol/sei-chain/sei-tendermint/autobahn/types"
	"github.com/sei-protocol/sei-chain/sei-tendermint/internal/autobahn/avail/metrics"
	"github.com/sei-protocol/sei-chain/sei-tendermint/internal/autobahn/consensus/persist"
	"github.com/sei-protocol/sei-chain/sei-tendermint/libs/utils"
)

// TODO: when dynamic committee changes are supported, newly joined members
// must be added to blocks, votes, nextBlockToPersist, and persistedBlockStart.
// Currently all four are initialized once in newInner from c.Lanes().All().
// BlockPersister creates lane WALs lazily inside MaybePruneAndPersistLane, but the new
// member must also appear in inner.blocks before the next persist cycle.
type inner struct {
	latestAppQC    utils.Option[*types.AppQC]
	latestCommitQC utils.AtomicSend[utils.Option[*types.CommitQC]]
	appVotes       *queue[types.GlobalBlockNumber, appVotes]
	commitQCs      *queue[types.RoadIndex, *types.CommitQC]
	blocks         map[types.LaneID]*queue[types.BlockNumber, *types.Signed[*types.LaneProposal]]
	votes          map[types.LaneID]*queue[types.BlockNumber, blockVotes]
	// nextBlockToPersist tracks per-lane how far block persistence has progressed.
	// RecvBatch only yields blocks below this cursor for voting.
	// Always initialized (even when persistence is disabled — the no-op persist
	// goroutine bumps it immediately). Not persisted to disk: on restart it is
	// reconstructed from the blocks already on disk (see newInner).
	//
	// TODO: consider giving this its own AtomicSend to avoid waking unrelated
	// inner waiters (PushVote, PushCommitQC, etc.) on markBlockPersisted calls.
	// Now that blocks are persisted concurrently by lane (one notification per
	// lane per batch, not per block), the frequency is lower, but still not
	// ideal. Only RecvBatch needs to be notified of cursor changes;
	// collectPersistBatch is in the same goroutine and reads it directly.
	nextBlockToPersist map[types.LaneID]types.BlockNumber

	// persistedBlockStart is the per-lane block number derived from the last
	// durably persisted prune anchor. Block admission (PushBlock, ProduceBlock,
	// WaitForCapacity, PushVote) uses persistedBlockStart + BlocksPerLane as
	// the capacity limit, ensuring we never admit more blocks than can be
	// recovered after a crash.
	persistedBlockStart map[types.LaneID]types.BlockNumber
}

// loadedAvailState holds data loaded from disk on restart.
// pruneAnchor is the decoded prune anchor (if any).
// commitQCs and blocks are pre-filtered: stale entries below the
// anchor have already been removed by loadPersistedState.
// commitQCs are sorted by road index; blocks are sorted by number per lane.
// newInner requires both to be contiguous and returns an error on gaps.
type loadedAvailState struct {
	pruneAnchor utils.Option[*PruneAnchor]
	commitQCs   []persist.LoadedCommitQC
	blocks      map[types.LaneID][]persist.LoadedBlock
}

func newInner(startEpochTrio types.EpochTrio, loaded utils.Option[*loadedAvailState]) (*inner, error) {
	lanes := startEpochTrio.CurrentAndNextLanes()
	votes := map[types.LaneID]*queue[types.BlockNumber, blockVotes]{}
	blocks := map[types.LaneID]*queue[types.BlockNumber, *types.Signed[*types.LaneProposal]]{}
	for lane := range lanes {
		votes[lane] = newQueue[types.BlockNumber, blockVotes]()
		blocks[lane] = newQueue[types.BlockNumber, *types.Signed[*types.LaneProposal]]()
	}

	i := &inner{
		latestAppQC:         utils.None[*types.AppQC](),
		latestCommitQC:      utils.NewAtomicSend(utils.None[*types.CommitQC]()),
		appVotes:            newQueue[types.GlobalBlockNumber, appVotes](),
		commitQCs:           newQueue[types.RoadIndex, *types.CommitQC](),
		blocks:              blocks,
		votes:               votes,
		nextBlockToPersist:  make(map[types.LaneID]types.BlockNumber, len(votes)),
		persistedBlockStart: make(map[types.LaneID]types.BlockNumber, len(votes)),
	}
	i.appVotes.prune(startEpochTrio.Current.FirstBlock())

	l, ok := loaded.Get()
	if !ok {
		return i, nil
	}

	// Apply the persisted prune anchor first: prune() positions all queues
	// (commitQCs, blocks, votes) so that subsequent pushBack calls insert
	// at the correct indices without needing reset().
	if anchor, ok := l.pruneAnchor.Get(); ok {
		logger.Info("loaded persisted prune anchor",
			slog.Uint64("roadIndex", uint64(anchor.AppQC.Proposal().RoadIndex())),
			slog.Uint64("globalNumber", uint64(anchor.AppQC.Proposal().GlobalNumber())),
		)
		if _, err := i.prune(anchor.AppQC, anchor.CommitQC); err != nil {
			return nil, fmt.Errorf("prune: %w", err)
		}
		for lane := range i.blocks {
			i.persistedBlockStart[lane] = anchor.CommitQC.LaneRange(lane).First()
		}
	}

	// Restore persisted CommitQCs. prune() may have already pushed the
	// anchor's CommitQC, so skip entries below commitQCs.next.
	for _, lqc := range l.commitQCs {
		if lqc.Index < i.commitQCs.next {
			continue
		}
		if lqc.Index != i.commitQCs.next {
			return nil, fmt.Errorf("non-contiguous persisted commitQCs: expected %d, got %d", i.commitQCs.next, lqc.Index)
		}
		i.commitQCs.pushBack(lqc.QC)
	}
	if i.commitQCs.next > i.commitQCs.first {
		i.latestCommitQC.Store(utils.Some(i.commitQCs.q[i.commitQCs.next-1]))
	}

	// Restore persisted blocks. Create queues on demand for any lane present
	// in the WAL — lanes outside the current epoch will be pruned by
	// advanceEpochLanes in NewState if a boundary was crossed.
	for lane, bs := range l.blocks {
		if len(bs) == 0 {
			continue
		}
		if _, ok := i.blocks[lane]; !ok {
			i.blocks[lane] = newQueue[types.BlockNumber, *types.Signed[*types.LaneProposal]]()
			i.votes[lane] = newQueue[types.BlockNumber, blockVotes]()
			i.nextBlockToPersist[lane] = 0
			i.persistedBlockStart[lane] = 0
		}
		q := i.blocks[lane]
		var lastHash types.BlockHeaderHash
		for j, b := range bs {
			if q.Len() >= BlocksPerLane {
				return nil, fmt.Errorf("lane %s: loaded %d blocks exceeds capacity %d", lane, len(bs), BlocksPerLane)
			}
			if b.Number != q.next {
				return nil, fmt.Errorf("lane %s: non-contiguous persisted blocks: expected %d, got %d", lane, q.next, b.Number)
			}
			if j > 0 {
				if got := b.Proposal.Msg().Block().Header().ParentHash(); got != lastHash {
					return nil, fmt.Errorf("lane %s: parent hash mismatch at block %d", lane, b.Number)
				}
			}
			lastHash = b.Proposal.Msg().Block().Header().Hash()
			q.pushBack(b.Proposal)
		}
		if q.next > q.first {
			i.nextBlockToPersist[lane] = q.next
		}
	}

	return i, nil
}

func (i *inner) laneQC(lane types.LaneID, n types.BlockNumber, trio types.EpochTrio) (*types.LaneQC, bool) {
	c := trio.Current.Committee()
	quorum := c.LaneQuorum()
	epIdx := trio.Current.EpochIndex()
	for _, byEpoch := range i.votes[lane].q[n].byHash {
		if set, ok := byEpoch[epIdx]; ok && set.weight >= quorum {
			return types.NewLaneQC(set.votes), true
		}
	}
	return nil, false
}

// advanceEpochLanes rotates the lane set for a crossing into nextTrio: it
// creates queues for lanes newly introduced by the next epoch, drops lanes no
// longer in the window, and back-fills the newly-entering Next epoch's vote
// sets. pushVote credits each vote to the Current and Next epochs only, so when
// nextTrio.Next first enters the window its sets are empty; applyEpoch seeds
// them from stored votes so a block that finalizes under it (a lagging lane, or
// an identical committee) still reaches quorum. The prior Current/Next sets
// were already filled by pushVote, so they need no adjustment.
func (i *inner) advanceEpochLanes(nextTrio types.EpochTrio) {
	activeLanes := nextTrio.CurrentAndNextLanes()
	for lane := range activeLanes {
		if _, ok := i.blocks[lane]; !ok {
			i.blocks[lane] = newQueue[types.BlockNumber, *types.Signed[*types.LaneProposal]]()
		}
		if _, ok := i.votes[lane]; !ok {
			i.votes[lane] = newQueue[types.BlockNumber, blockVotes]()
		}
		if _, ok := i.nextBlockToPersist[lane]; !ok {
			i.nextBlockToPersist[lane] = 0
		}
		if _, ok := i.persistedBlockStart[lane]; !ok {
			i.persistedBlockStart[lane] = 0
		}
	}
	// Retain Prev-epoch lanes in the deletion guard: fullCommitQC may still be
	// collecting headers from the boundary QC that triggered this advance, and
	// it accesses lane queues by epoch N's committee. Prev lanes are cleaned up
	// naturally on the next epoch advance when they fall outside AllLanes().
	retainLanes := nextTrio.AllLanes()
	for lane := range i.blocks {
		if _, ok := retainLanes[lane]; !ok {
			delete(i.blocks, lane)
			delete(i.votes, lane)
			delete(i.nextBlockToPersist, lane)
			delete(i.persistedBlockStart, lane)
		}
	}
	// Seed the newly-entering Next epoch's vote sets from votes already stored.
	for _, voteQueue := range i.votes {
		for n := voteQueue.first; n < voteQueue.next; n++ {
			voteQueue.q[n].applyEpoch(nextTrio.Next)
		}
	}
}

// prune advances the state to account for a new AppQC/CommitQC pair.
// Returns true if pruning occurred, false if the QC was stale.
func (i *inner) prune(appQC *types.AppQC, commitQC *types.CommitQC) (bool, error) {
	idx := appQC.Proposal().RoadIndex()
	if idx != commitQC.Proposal().Index() {
		return false, fmt.Errorf("mismatched QCs: appQC index %v, commitQC index %v", idx, commitQC.Proposal().Index())
	}
	if idx < types.NextOpt(i.latestAppQC) {
		return false, nil
	}
	i.latestAppQC = utils.Some(appQC)
	metrics.ObserveAppQC(appQC)
	i.commitQCs.prune(idx)
	if i.commitQCs.next == idx {
		i.commitQCs.pushBack(commitQC)
		metrics.ObserveCommitQC(commitQC)
	}
	i.appVotes.prune(commitQC.GlobalRange().First)
	for lane := range i.votes {
		lr := commitQC.LaneRange(lane)
		i.votes[lr.Lane()].prune(lr.First())
		i.blocks[lr.Lane()].prune(lr.First())
		if i.nextBlockToPersist[lr.Lane()] < lr.First() {
			i.nextBlockToPersist[lr.Lane()] = lr.First()
		}
	}
	return true, nil
}
