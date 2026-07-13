package epoch

import (
	"fmt"
	"time"

	"github.com/sei-protocol/sei-chain/sei-tendermint/autobahn/types"
	"github.com/sei-protocol/sei-chain/sei-tendermint/libs/utils"
)

// EpochLength is the number of road indices per epoch.
const EpochLength types.RoadIndex = 108_000

type registryState struct {
	m       map[types.EpochIndex]*types.Epoch
	latest  types.EpochIndex
	seeding bool // true until SealSeeding is called
}

// Registry is the authoritative source of epoch and committee information.
// All layers (consensus, data, avail) read from it.
type Registry struct {
	state utils.RWMutex[*registryState]
}

// NewRegistry creates a Registry with the genesis committee.
func NewRegistry(
	committee *types.Committee,
	firstBlock types.GlobalBlockNumber,
	genesisTimestamp time.Time,
) (*Registry, error) {
	ep := types.NewEpoch(0, types.RoadRange{First: 0, Last: EpochLength - 1}, genesisTimestamp, committee, firstBlock)
	return &Registry{
		state: utils.NewRWMutex(&registryState{
			m:       map[types.EpochIndex]*types.Epoch{0: ep},
			latest:  0,
			seeding: true,
		}),
	}, nil
}

// SealSeeding marks the end of the initialization seeding phase. After this
// call, EpochAt will no longer auto-generate missing epochs; new epochs can
// only be created via AdvanceIfNeeded (driven by block execution).
//
// Must be called once, after all layers (data, avail, consensus) have finished
// their NewState initialization.
//
// TODO: during the seeding phase, epochs are generated with the genesis
// committee as a placeholder. Once epoch state is included in snapshots,
// replace this with reconstruction from the snapshot so that restarted nodes
// use the real per-epoch committees immediately.
func (r *Registry) SealSeeding() {
	for s := range r.state.Lock() {
		s.seeding = false
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
// During the seeding phase (before SealSeeding), missing epochs are
// auto-generated with the genesis committee. After seeding, returns an error
// if the epoch has not been registered via AdvanceIfNeeded.
func (r *Registry) EpochAt(roadIndex types.RoadIndex) (*types.Epoch, error) {
	epochIdx := types.EpochIndex(roadIndex / EpochLength)
	for s := range r.state.Lock() {
		if ep, ok := s.m[epochIdx]; ok {
			return ep, nil
		}
		if !s.seeding {
			return nil, fmt.Errorf("epoch %d (road %d) not registered", epochIdx, roadIndex)
		}
		ep, _ := r.makeEpoch(s, epochIdx) //nolint:errcheck // genesis always present
		if ep == nil {
			return nil, fmt.Errorf("epoch %d (road %d) not registered", epochIdx, roadIndex)
		}
		return ep, nil
	}
	panic("unreachable")
}

// makeEpoch constructs a new epoch at epochIdx using the genesis committee and
// inserts it into s. Caller must hold the write lock.
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
	return epoch, nil
}

// AdvanceIfNeeded seeds epoch N+2 when a block in epoch N is executed.
// Called by executeBlock (giga_router_common.go) on both validator and full-node paths.
//
// Seeding model: execution of any block in epoch N seeds epoch N+2 as a
// placeholder (genesis committee). Epoch N+1 is seeded by executing epoch N-1
// blocks (same rule applied one epoch earlier). The midpoint liveness gate
// (consensus/inner.go) checks that N+2 is present before voting at MidPoint(N),
// ensuring TrioAt(N.Last+1) — which needs N+2 as Next — always succeeds.
//
// TODO: real committee rotation — pass the derived committee for N+2 here once
// the execution layer computes it from the last block of epoch N.
func (r *Registry) AdvanceIfNeeded(roadIndex types.RoadIndex) {
	nextNextIdx := types.EpochIndex(roadIndex/EpochLength) + 2
	for s := range r.state.Lock() {
		if _, ok := s.m[nextNextIdx]; !ok {
			_, _ = r.makeEpoch(s, nextNextIdx) //nolint:errcheck // genesis always present
		}
	}
}

// TrioAt returns the EpochTrio centered on the epoch containing roadIndex.
// During the seeding phase, missing epochs are auto-generated (same as EpochAt).
// Returns an error if Current or Next is not in the registry after seeding.
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
		trio.Prev, _ = r.EpochAt(types.RoadIndex(centerIdx-1) * EpochLength)
	}
	return trio, nil
}
