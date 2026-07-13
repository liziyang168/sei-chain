package epoch

import (
	"testing"
	"time"

	"github.com/sei-protocol/sei-chain/sei-tendermint/autobahn/types"
	"github.com/sei-protocol/sei-chain/sei-tendermint/libs/utils"
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
	r.SealSeeding()
	_, err := r.EpochAt(EpochLength)
	if err == nil {
		t.Fatal("EpochAt(EpochLength) expected error for unregistered epoch, got nil")
	}
}

func TestEpochAt_FoundAfterAdvanceIfNeeded(t *testing.T) {
	r, _ := makeRegistry(t)
	rng := utils.TestRng()
	key := types.GenSecretKey(rng)
	proposal := types.NewAppProposal(0, EpochLength-1, types.AppHash{}, types.EpochIndex(0))
	appQC := types.NewAppQC([]*types.Signed[*types.AppVote]{
		types.Sign(key, types.NewAppVote(proposal)),
	})
	r.AdvanceIfNeeded(appQC)
	ep, err := r.EpochAt(EpochLength)
	if err != nil {
		t.Fatalf("EpochAt(EpochLength) after AdvanceIfNeeded: %v", err)
	}
	if ep.EpochIndex() != 1 {
		t.Fatalf("EpochAt(EpochLength).EpochIndex() = %d, want 1", ep.EpochIndex())
	}
}

func TestSeeding_AutoGeneratesEpochs(t *testing.T) {
	r, _ := makeRegistry(t)
	for _, idx := range []types.EpochIndex{4, 5, 6} {
		ep, err := r.EpochAt(types.RoadIndex(idx) * EpochLength)
		if err != nil {
			t.Fatalf("EpochAt(epoch %d) failed during seeding: %v", idx, err)
		}
		if ep.EpochIndex() != idx {
			t.Fatalf("EpochAt(epoch %d).EpochIndex() = %d, want %d", idx, ep.EpochIndex(), idx)
		}
	}
}

func TestSeeding_DoesNotOverwriteExisting(t *testing.T) {
	r, _ := makeRegistry(t)
	genesis, _ := r.EpochAt(0)
	after, _ := r.EpochAt(0)
	if genesis != after {
		t.Fatal("seeding phase overwrote existing genesis epoch")
	}
}

func TestAdvanceIfNeeded_AdvancesLatest(t *testing.T) {
	r, _ := makeRegistry(t)
	rng := utils.TestRng()
	key := types.GenSecretKey(rng)
	proposal := types.NewAppProposal(0, EpochLength-1, types.AppHash{}, types.EpochIndex(0))
	appQC := types.NewAppQC([]*types.Signed[*types.AppVote]{
		types.Sign(key, types.NewAppVote(proposal)),
	})
	r.AdvanceIfNeeded(appQC)
	if latest := r.LatestEpoch().EpochIndex(); latest != 0 {
		t.Fatalf("LatestEpoch().EpochIndex() = %d after AdvanceIfNeeded in epoch 0, want 0", latest)
	}
}

func TestSeeding_LatestNotAdvanced(t *testing.T) {
	r, _ := makeRegistry(t)
	_, _ = r.EpochAt(5 * EpochLength)
	latest := r.LatestEpoch()
	if latest.EpochIndex() != 0 {
		t.Fatalf("LatestEpoch().EpochIndex() = %d after seeding, want 0", latest.EpochIndex())
	}
}

func TestSealSeeding_BlocksAutoGenerate(t *testing.T) {
	r, _ := makeRegistry(t)
	r.SealSeeding()
	_, err := r.EpochAt(5 * EpochLength)
	if err == nil {
		t.Fatal("EpochAt should return error after SealSeeding for unregistered epoch")
	}
}

// --- TrioAt ---

func TestTrioAt_GenesisEpoch(t *testing.T) {
	r, _ := makeRegistry(t)
	trio, err := r.TrioAt(0)
	if err != nil {
		t.Fatalf("TrioAt(0) error: %v", err)
	}
	if trio.Prev != nil {
		t.Fatalf("TrioAt(0).Prev = %v, want nil for epoch 0", trio.Prev)
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
	// During seeding, TrioAt auto-generates any missing neighbor epochs.
	trio, err := r.TrioAt(2 * EpochLength)
	if err != nil {
		t.Fatalf("TrioAt(epoch 2) error: %v", err)
	}
	if trio.Prev == nil || trio.Prev.EpochIndex() != 1 {
		t.Fatalf("TrioAt(epoch 2).Prev.EpochIndex() wrong, want 1")
	}
	if trio.Current == nil || trio.Current.EpochIndex() != 2 {
		t.Fatalf("TrioAt(epoch 2).Current.EpochIndex() wrong, want 2")
	}
	if trio.Next == nil || trio.Next.EpochIndex() != 3 {
		t.Fatalf("TrioAt(epoch 2).Next.EpochIndex() wrong, want 3")
	}
}

func TestTrioAt_ErrorWhenNextMissingAfterSealing(t *testing.T) {
	r, _ := makeRegistry(t)
	r.SealSeeding()
	// epoch 0 is present but epoch 1 is not — TrioAt should error.
	_, err := r.TrioAt(0)
	if err == nil {
		t.Fatal("TrioAt(0) expected error when Next epoch not registered after sealing, got nil")
	}
}
