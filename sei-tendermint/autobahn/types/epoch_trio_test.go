package types_test

import (
	"errors"
	"testing"

	"github.com/sei-protocol/sei-chain/sei-tendermint/autobahn/types"
	"github.com/sei-protocol/sei-chain/sei-tendermint/libs/utils"
)

// makeThreeEpochs returns three consecutive epochs with non-overlapping road ranges.
func makeThreeEpochs(t *testing.T) (prev, current, next *types.Epoch, keys []types.SecretKey) {
	t.Helper()
	rng := utils.TestRng()
	sks := utils.GenSliceN(rng, 3, types.GenSecretKey)
	weights := map[types.PublicKey]uint64{}
	for _, sk := range sks {
		weights[sk.Public()] = 1
	}
	committee := utils.OrPanic1(types.NewCommittee(weights))
	prev = types.NewEpoch(0, types.RoadRange{First: 0, Last: 99}, utils.GenTimestamp(rng), committee, 1)
	current = types.NewEpoch(1, types.RoadRange{First: 100, Last: 199}, utils.GenTimestamp(rng), committee, 101)
	next = types.NewEpoch(2, types.RoadRange{First: 200, Last: 299}, utils.GenTimestamp(rng), committee, 201)
	return prev, current, next, sks
}

// --- EpochForRoad ---

func TestEpochForRoad_HitsCurrentEpoch(t *testing.T) {
	_, current, next, _ := makeThreeEpochs(t)
	w := types.EpochTrio{Current: current, Next: next}
	ep, err := w.EpochForRoad(150)
	if err != nil {
		t.Fatalf("EpochForRoad(150): %v", err)
	}
	if ep.EpochIndex() != current.EpochIndex() {
		t.Fatalf("got epoch %d, want current (%d)", ep.EpochIndex(), current.EpochIndex())
	}
}

func TestEpochForRoad_HitsPrevEpoch(t *testing.T) {
	prev, current, next, _ := makeThreeEpochs(t)
	w := types.EpochTrio{Prev: prev, Current: current, Next: next}
	ep, err := w.EpochForRoad(50)
	if err != nil {
		t.Fatalf("EpochForRoad(50): %v", err)
	}
	if ep.EpochIndex() != prev.EpochIndex() {
		t.Fatalf("got epoch %d, want prev (%d)", ep.EpochIndex(), prev.EpochIndex())
	}
}

func TestEpochForRoad_HitsNextEpoch(t *testing.T) {
	_, current, next, _ := makeThreeEpochs(t)
	w := types.EpochTrio{Current: current, Next: next}
	ep, err := w.EpochForRoad(250)
	if err != nil {
		t.Fatalf("EpochForRoad(250): %v", err)
	}
	if ep.EpochIndex() != next.EpochIndex() {
		t.Fatalf("got epoch %d, want next (%d)", ep.EpochIndex(), next.EpochIndex())
	}
}

func TestEpochForRoad_OutsideWindowReturnsError(t *testing.T) {
	_, current, next, _ := makeThreeEpochs(t)
	w := types.EpochTrio{Current: current, Next: next}
	if _, err := w.EpochForRoad(999); err == nil {
		t.Fatal("EpochForRoad(999) expected error, got nil")
	}
}

func TestEpochForRoad_OpenRangePrevDoesNotMaskCurrent(t *testing.T) {
	// Regression: genesis epoch used to have OpenRoadRange (Last=MaxUint64).
	// EpochForRoad iterated [Prev, Current, Next], so Prev.Has(any) was always
	// true — every lookup returned epoch 0 instead of the correct epoch.
	rng := utils.TestRng()
	sks := utils.GenSliceN(rng, 3, types.GenSecretKey)
	weights := map[types.PublicKey]uint64{}
	for _, sk := range sks {
		weights[sk.Public()] = 1
	}
	committee := utils.OrPanic1(types.NewCommittee(weights))
	openEpoch := types.NewEpoch(0, types.OpenRoadRange(), utils.GenTimestamp(rng), committee, 1)
	current := types.NewEpoch(1, types.RoadRange{First: 100, Last: 199}, utils.GenTimestamp(rng), committee, 101)
	w := types.EpochTrio{Prev: openEpoch, Current: current}
	ep, err := w.EpochForRoad(150)
	if err != nil {
		t.Fatalf("EpochForRoad(150): %v", err)
	}
	if ep.EpochIndex() != current.EpochIndex() {
		t.Fatalf("got epoch %d (Prev with OpenRoadRange masked Current), want current (%d)",
			ep.EpochIndex(), current.EpochIndex())
	}
}

func TestEpochForRoad_NilPrevSkipped(t *testing.T) {
	_, current, next, _ := makeThreeEpochs(t)
	w := types.EpochTrio{Prev: nil, Current: current, Next: next}
	// Road 50 belongs to prev, which is nil — should return error, not panic.
	if _, err := w.EpochForRoad(50); err == nil {
		t.Fatal("EpochForRoad(50) with nil Prev expected error, got nil")
	}
}

// --- CurrentAndNextLanes ---

func TestCurrentAndNextLanes_UnionOfBoth(t *testing.T) {
	_, current, next, _ := makeThreeEpochs(t)
	w := types.EpochTrio{Current: current, Next: next}
	lanes := w.CurrentAndNextLanes()
	for lane := range current.Committee().Lanes().All() {
		if _, ok := lanes[lane]; !ok {
			t.Fatalf("lane %v from Current missing from result", lane)
		}
	}
	for lane := range next.Committee().Lanes().All() {
		if _, ok := lanes[lane]; !ok {
			t.Fatalf("lane %v from Next missing from result", lane)
		}
	}
}

func TestCurrentAndNextLanes_NilNextOmitted(t *testing.T) {
	_, current, _, _ := makeThreeEpochs(t)
	w := types.EpochTrio{Current: current, Next: nil}
	lanes := w.CurrentAndNextLanes()
	for lane := range current.Committee().Lanes().All() {
		if _, ok := lanes[lane]; !ok {
			t.Fatalf("lane %v from Current missing from result", lane)
		}
	}
}

// --- VerifyInWindow ---

func TestVerifyInWindow_MatchesCurrent(t *testing.T) {
	_, current, next, _ := makeThreeEpochs(t)
	w := types.EpochTrio{Current: current, Next: next}
	ep, err := w.VerifyInWindow(func(c *types.Committee) error { return nil })
	if err != nil {
		t.Fatalf("VerifyInWindow: %v", err)
	}
	if ep.EpochIndex() != current.EpochIndex() {
		t.Fatalf("got epoch %d, want current (%d)", ep.EpochIndex(), current.EpochIndex())
	}
}

func TestVerifyInWindow_FallsBackToNext(t *testing.T) {
	_, current, next, _ := makeThreeEpochs(t)
	w := types.EpochTrio{Current: current, Next: next}
	first := true
	ep, err := w.VerifyInWindow(func(*types.Committee) error {
		if first {
			first = false
			return errors.New("reject")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("VerifyInWindow: %v", err)
	}
	if ep.EpochIndex() != next.EpochIndex() {
		t.Fatalf("got epoch %d, want next (%d)", ep.EpochIndex(), next.EpochIndex())
	}
}

func TestVerifyInWindow_NoneMatchReturnsError(t *testing.T) {
	_, current, next, _ := makeThreeEpochs(t)
	w := types.EpochTrio{Current: current, Next: next}
	if _, err := w.VerifyInWindow(func(*types.Committee) error { return errors.New("reject") }); err == nil {
		t.Fatal("VerifyInWindow expected error when fn rejects all, got nil")
	}
}

func TestVerifyInWindow_SkipsPrev(t *testing.T) {
	prev, current, next, _ := makeThreeEpochs(t)
	w := types.EpochTrio{Prev: prev, Current: current, Next: next}
	callCount := 0
	_, _ = w.VerifyInWindow(func(*types.Committee) error {
		callCount++
		return errors.New("reject")
	})
	// fn should be called for Current and Next only — not Prev.
	if callCount != 2 {
		t.Fatalf("VerifyInWindow called fn %d times, want 2 (Current + Next only)", callCount)
	}
}
