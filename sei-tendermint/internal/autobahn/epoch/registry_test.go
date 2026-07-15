package epoch

import (
	"testing"
	"time"

	"github.com/sei-protocol/sei-chain/sei-tendermint/autobahn/types"
	"github.com/sei-protocol/sei-chain/sei-tendermint/libs/utils"
	"github.com/stretchr/testify/require"
)

func makeRegistry(t *testing.T) (*Registry, *types.Committee) {
	t.Helper()
	rng := utils.TestRng()
	committee := utils.OrPanic1(types.NewCommittee(map[types.PublicKey]uint64{
		types.GenSecretKey(rng).Public(): 1,
		types.GenSecretKey(rng).Public(): 1,
		types.GenSecretKey(rng).Public(): 1,
	}))
	r := utils.OrPanic1(NewRegistry(committee, 0, time.Time{}))
	return r, committee
}

func TestNewRegistry_GenesisEpochBoundedRange(t *testing.T) {
	r, _ := makeRegistry(t)
	ep, err := r.EpochAt(0)
	if err != nil {
		t.Fatalf("EpochAt(0): %v", err)
	}
	rng := ep.RoadRange()
	if rng.First != 0 || rng.Last != EpochLength-1 {
		t.Fatalf("genesis RoadRange = {%d, %d}, want {0, %d}", rng.First, rng.Last, EpochLength-1)
	}
}

func TestEpochAt_GenesisEpoch(t *testing.T) {
	r, _ := makeRegistry(t)
	ep, err := r.EpochAt(0)
	if err != nil {
		t.Fatalf("EpochAt(0) error: %v", err)
	}
	if ep.EpochIndex() != 0 {
		t.Fatalf("EpochAt(0).EpochIndex() = %d, want 0", ep.EpochIndex())
	}
}

func TestEpochAt_WithinGenesisEpoch(t *testing.T) {
	r, _ := makeRegistry(t)
	ep, err := r.EpochAt(EpochLength - 1)
	if err != nil {
		t.Fatalf("EpochAt(EpochLength-1) error: %v", err)
	}
	if ep.EpochIndex() != 0 {
		t.Fatalf("EpochAt(EpochLength-1).EpochIndex() = %d, want 0", ep.EpochIndex())
	}
}

func TestEpochAt_ErrorIfNotRegistered(t *testing.T) {
	r, _ := makeRegistry(t)
	_, err := r.EpochAt(2 * EpochLength)
	if err == nil {
		t.Fatal("EpochAt(2*EpochLength) expected error for unregistered epoch, got nil")
	}
}

func TestEpochAt_FoundAfterAdvanceIfNeeded(t *testing.T) {
	r, _ := makeRegistry(t)
	// Executing any block in epoch 0 seeds epoch 2 (N+2).
	r.AdvanceIfNeeded(EpochLength - 1)
	ep, err := r.EpochAt(2 * EpochLength)
	if err != nil {
		t.Fatalf("EpochAt(2*EpochLength) after AdvanceIfNeeded: %v", err)
	}
	if ep.EpochIndex() != 2 {
		t.Fatalf("EpochAt(2*EpochLength).EpochIndex() = %d, want 2", ep.EpochIndex())
	}
}

func TestSetupInitialTrio_WindowAroundTip(t *testing.T) {
	r, _ := makeRegistry(t)
	// N=5 → {3,4,5,6}. Epochs 0,1 already present from NewRegistry.
	r.SetupInitialTrio(5 * EpochLength)
	for _, idx := range []types.EpochIndex{3, 4, 5, 6} {
		ep, err := r.EpochAt(types.RoadIndex(idx) * EpochLength)
		if err != nil {
			t.Fatalf("EpochAt(epoch %d) after SetupInitialTrio: %v", idx, err)
		}
		if ep.EpochIndex() != idx {
			t.Fatalf("EpochAt(epoch %d).EpochIndex() = %d, want %d", idx, ep.EpochIndex(), idx)
		}
	}
	if _, err := r.EpochAt(2 * EpochLength); err == nil {
		t.Fatal("EpochAt(epoch 2) should not be present after SetupInitialTrio(5*EpochLength)")
	}
	if _, err := r.EpochAt(7 * EpochLength); err == nil {
		t.Fatal("EpochAt(epoch 7) should not be present after SetupInitialTrio(5*EpochLength)")
	}
}

// --- TrioAt ---

func TestTrioAt_GenesisEpoch(t *testing.T) {
	r, _ := makeRegistry(t)
	trio, err := r.TrioAt(0)
	if err != nil {
		t.Fatalf("TrioAt(0) error: %v", err)
	}
	if trio.Prev.IsPresent() {
		t.Fatalf("TrioAt(0).Prev = %v, want absent for epoch 0", trio.Prev)
	}
	if trio.Current == nil || trio.Current.EpochIndex() != 0 {
		t.Fatalf("TrioAt(0).Current.EpochIndex() wrong, want 0")
	}
	if trio.Next == nil || trio.Next.EpochIndex() != 1 {
		t.Fatalf("TrioAt(0).Next.EpochIndex() = %v, want 1", trio.Next)
	}
}

func TestTrioAt_MiddleEpoch(t *testing.T) {
	r, _ := makeRegistry(t)
	r.SetupInitialTrio(2 * EpochLength)
	trio, err := r.TrioAt(2 * EpochLength)
	if err != nil {
		t.Fatalf("TrioAt(epoch 2) error: %v", err)
	}
	prev, ok := trio.Prev.Get()
	if !ok || prev.EpochIndex() != 1 {
		t.Fatalf("TrioAt(epoch 2).Prev.EpochIndex() wrong, want 1")
	}
	if trio.Current == nil || trio.Current.EpochIndex() != 2 {
		t.Fatalf("TrioAt(epoch 2).Current.EpochIndex() wrong, want 2")
	}
	if trio.Next == nil || trio.Next.EpochIndex() != 3 {
		t.Fatalf("TrioAt(epoch 2).Next.EpochIndex() wrong, want 3")
	}
}

func TestTrioAt_ErrorWhenNextMissing(t *testing.T) {
	// SetupInitialTrio always seeds Next, so leave a hole by building a bare
	// registry with only epoch 0 (skipping NewRegistry's SetupInitialTrio(0)).
	committee := utils.OrPanic1(types.NewCommittee(map[types.PublicKey]uint64{
		types.GenSecretKey(utils.TestRng()).Public(): 1,
	}))
	ep := types.NewEpoch(0, types.RoadRange{First: 0, Last: EpochLength - 1}, time.Time{}, committee, 0)
	bare := &Registry{
		state: utils.NewRWMutex(&registryState{
			m:      map[types.EpochIndex]*types.Epoch{0: ep},
			latest: 0,
		}),
		highestEpoch: utils.NewAtomicSend(types.EpochIndex(0)),
	}
	_, err := bare.TrioAt(0)
	if err == nil {
		t.Fatal("TrioAt(0) expected error when Next epoch not registered, got nil")
	}
}

func TestWaitForTrio_FastPathAndWait(t *testing.T) {
	r, _ := makeRegistry(t)
	// NewRegistry SetupInitialTrio(0) → {0,1}; TrioAt(0) is immediate.
	trio, err := r.WaitForTrio(t.Context(), 0)
	require.NoError(t, err)
	require.Equal(t, types.EpochIndex(0), trio.Current.EpochIndex())

	// Tipcut into epoch 1 needs epoch 2. Seed after WaitForTrio is blocked.
	tip := EpochLength
	_, err = r.TrioAt(tip)
	require.Error(t, err)

	type result struct {
		trio types.EpochTrio
		err  error
	}
	done := make(chan result, 1)
	go func() {
		trio, err := r.WaitForTrio(t.Context(), tip)
		done <- result{trio, err}
	}()
	r.AdvanceIfNeeded(0) // seeds epoch 2
	got := <-done
	require.NoError(t, got.err)
	require.Equal(t, types.EpochIndex(1), got.trio.Current.EpochIndex())
	require.Equal(t, types.EpochIndex(2), got.trio.Next.EpochIndex())
}
