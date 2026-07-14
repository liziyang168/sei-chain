package epoch

import (
	"context"
	"fmt"
	"time"

	"github.com/sei-protocol/sei-chain/sei-tendermint/autobahn/types"
	"github.com/sei-protocol/sei-chain/sei-tendermint/libs/utils"
)

// EpochLength is the number of road indices per epoch.
const EpochLength types.RoadIndex = 108_000

type registryState struct {
	m      map[types.EpochIndex]*types.Epoch
	latest types.EpochIndex
}

// Registry is the authoritative source of epoch and committee information.
// All layers (consensus, data, avail) read from it.
type Registry struct {
	state utils.RWMutex[*registryState]
	// highestEpoch is the highest epoch index registered so far. It only ever
	// increases (epochs are registered contiguously from the current operating
	// point), so WaitForEpoch can block until it reaches a target index. Kept
	// separate from state so the RLock read fast-path on state is preserved.
	highestEpoch utils.AtomicSend[types.EpochIndex]
}

// NewRegistry creates a Registry with the genesis committee and seeds epoch 1
// so TrioAt(0) succeeds on a fresh node.
func NewRegistry(
	committee *types.Committee,
	firstBlock types.GlobalBlockNumber,
	genesisTimestamp time.Time,
) (*Registry, error) {
	ep := types.NewEpoch(0, types.RoadRange{First: 0, Last: EpochLength - 1}, genesisTimestamp, committee, firstBlock)
	r := &Registry{
		state: utils.NewRWMutex(&registryState{
			m:      map[types.EpochIndex]*types.Epoch{0: ep},
			latest: 0,
		}),
		highestEpoch: utils.NewAtomicSend(types.EpochIndex(0)),
	}
	// Fresh start needs Current+Next for TrioAt(0).
	// TODO: in the future this information will be read from disk and verified
	// (snapshots / state sync); until then seed a genesis placeholder trio.
	r.SetupInitialTrio(0)
	return r, nil
}

// WaitForEpoch blocks until epochIdx has been registered, or ctx is done.
//
// Waiting on the single highestEpoch value is sufficient for the live path:
// AdvanceIfNeeded registers epochs contiguously ahead of execution (N → N+2),
// and callers only wait for Current.EpochIndex()+2. SetupInitialTrio may also
// raise highestEpoch while leaving gaps below the restart tip; those gaps are
// never WaitForEpoch targets (callers do not wait for epochs behind tip).
//
// Used at an epoch boundary: a node whose CommitQC stream has outrun its own
// block execution waits here rather than failing, and unblocks once execution
// catches up.
//
// CALLER CONTRACT: only block execution (AdvanceIfNeeded -> makeEpoch) advances
// highestEpoch for live waits, so callers MUST NOT hold any lock on that path —
// notably the avail/data inner lock — or the wake can never fire. WaitForEpoch
// itself holds no registry lock while blocked (it waits on the highestEpoch channel).
func (r *Registry) WaitForEpoch(ctx context.Context, epochIdx types.EpochIndex) error {
	_, err := r.highestEpoch.Subscribe().Wait(ctx, func(highest types.EpochIndex) bool {
		return highest >= epochIdx
	})
	return err
}

// SetupInitialTrio registers placeholder epochs around roadIndex's epoch N:
// N-2, N-1, N, and N+1 (clamped at 0). That covers TrioAt(roadIndex) (Current+Next)
// and leaves room for a lagging startTrio (e.g. avail prune anchor) up to two
// epochs behind the tip. Epochs already present are left unchanged. Placeholders
// reuse the genesis committee.
//
// Called from data.NewState after peeking the CommitQC WAL tip.
func (r *Registry) SetupInitialTrio(roadIndex types.RoadIndex) {
	n := types.EpochIndex(roadIndex / EpochLength)
	first := types.EpochIndex(0)
	if n >= 2 {
		first = n - 2
	}
	last := n + 1
	for s := range r.state.Lock() {
		for idx := first; idx <= last; idx++ {
			if _, ok := s.m[idx]; ok {
				continue
			}
			_, _ = r.makeEpoch(s, idx) //nolint:errcheck // genesis always present
		}
	}
}

// FirstBlock returns the first global block number of the genesis epoch.
// Used as the cold-start default (no WAL, no snapshot); WAL overrides this on restart.
func (r *Registry) FirstBlock() types.GlobalBlockNumber {
	for s := range r.state.RLock() {
		return s.m[0].FirstBlock()
	}
	panic("unreachable")
}

// EpochAt returns the epoch for the given road index.
// Returns an error if the epoch has not been registered via SetupInitialTrio or
// AdvanceIfNeeded.
func (r *Registry) EpochAt(roadIndex types.RoadIndex) (*types.Epoch, error) {
	epochIdx := types.EpochIndex(roadIndex / EpochLength)
	for s := range r.state.RLock() {
		if ep, ok := s.m[epochIdx]; ok {
			return ep, nil
		}
		return nil, fmt.Errorf("epoch %d (road %d) not registered", epochIdx, roadIndex)
	}
	panic("unreachable")
}

// makeEpoch constructs a new epoch at epochIdx using the genesis committee and
// inserts it into s. Caller must hold the write lock. Overwrites if present;
// callers that must not clobber should check existence first.
// Note: does NOT advance s.latest.
func (r *Registry) makeEpoch(s *registryState, epochIdx types.EpochIndex) (*types.Epoch, error) {
	genesis, ok := s.m[0]
	if !ok {
		return nil, fmt.Errorf("genesis epoch missing from registry")
	}
	firstRoad := types.RoadIndex(uint64(epochIdx) * uint64(EpochLength))
	lastRoad := firstRoad + EpochLength - 1
	epoch := types.NewEpoch(epochIdx, types.RoadRange{First: firstRoad, Last: lastRoad}, genesis.FirstTimestamp(), genesis.Committee(), genesis.FirstBlock())
	s.m[epochIdx] = epoch
	// Wake WaitForEpoch waiters. makeEpoch runs under the write lock, so this
	// Load/Store is serialized; highestEpoch only advances.
	if epochIdx > r.highestEpoch.Load() {
		r.highestEpoch.Store(epochIdx)
	}
	return epoch, nil
}

// AdvanceIfNeeded seeds epoch N+2 when a block in epoch N is executed.
// Called by executeBlock (giga_router_common.go) on both validator and full-node paths.
//
// Seeding model: execution of any block in epoch N seeds epoch N+2 as a
// placeholder (genesis committee). Epoch N+1 is seeded by executing epoch N-1
// blocks (same rule applied one epoch earlier), or by SetupInitialTrio at startup.
// At the epoch boundary avail.PushCommitQC needs TrioAt(N.Last+1), which requires
// N+2 as Next; if a node's CommitQC stream has outrun its execution and N+2 is
// not yet seeded, it blocks on WaitForEpoch until execution seeds it here.
//
// TODO: real committee rotation — pass the derived committee for N+2 here once
// the execution layer computes it from the last block of epoch N.
func (r *Registry) AdvanceIfNeeded(roadIndex types.RoadIndex) {
	nextNextIdx := types.EpochIndex(roadIndex/EpochLength) + 2
	// Fast path: epoch already seeded (common after the first block of the epoch).
	for s := range r.state.RLock() {
		if _, ok := s.m[nextNextIdx]; ok {
			return
		}
	}
	for s := range r.state.Lock() {
		if _, ok := s.m[nextNextIdx]; !ok {
			_, _ = r.makeEpoch(s, nextNextIdx) //nolint:errcheck // genesis always present
		}
	}
}

// TrioAt returns the EpochTrio centered on the epoch containing roadIndex.
// Current and Next must already be present in the registry (callers seed them);
// returns an error if either is missing. Prev is absent only when Current is epoch 0.
func (r *Registry) TrioAt(roadIndex types.RoadIndex) (types.EpochTrio, error) {
	centerIdx := types.EpochIndex(roadIndex / EpochLength)
	current, err := r.EpochAt(types.RoadIndex(centerIdx) * EpochLength)
	if err != nil {
		return types.EpochTrio{}, fmt.Errorf("epoch %d (road %d) not in registry", centerIdx, roadIndex)
	}
	next, err := r.EpochAt(types.RoadIndex(centerIdx+1) * EpochLength)
	if err != nil {
		return types.EpochTrio{}, fmt.Errorf("next epoch %d not in registry", centerIdx+1)
	}
	trio := types.EpochTrio{Current: current, Next: next}
	if centerIdx > 0 {
		if prev, err := r.EpochAt(types.RoadIndex(centerIdx-1) * EpochLength); err == nil {
			trio.Prev = utils.Some(prev)
		}
	}
	return trio, nil
}
