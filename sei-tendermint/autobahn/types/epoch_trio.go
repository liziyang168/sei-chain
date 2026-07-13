package types

import (
	"fmt"

	"github.com/sei-protocol/sei-chain/sei-tendermint/libs/utils"
)

// EpochTrio is a view of up to three consecutive epochs centered on Current.
// Current and Next are always present; Prev may be absent (epoch 0).
// Updated only when an AppQC advances into a new epoch.
type EpochTrio struct {
	Prev    utils.Option[*Epoch] // absent if Current is epoch 0
	Current *Epoch
	Next    *Epoch
}

// all returns the three epochs in priority order: Current first, then Next, then Prev.
// This ensures EpochForRoad matches the most-likely epoch first and prevents an
// open-range Prev from shadowing Current or Next.
func (w EpochTrio) all() [3]utils.Option[*Epoch] {
	return [3]utils.Option[*Epoch]{utils.Some(w.Current), utils.Some(w.Next), w.Prev}
}

// EpochForRoad returns the epoch whose road range contains roadIdx.
func (w EpochTrio) EpochForRoad(roadIdx RoadIndex) (*Epoch, error) {
	for _, oep := range w.all() {
		if ep, ok := oep.Get(); ok && ep.RoadRange().Has(roadIdx) {
			return ep, nil
		}
	}
	return nil, fmt.Errorf("road %d not in window %v", roadIdx, w)
}

// CurrentAndNextLanes returns the union of lanes for the current and next epochs.
func (w EpochTrio) CurrentAndNextLanes() map[LaneID]struct{} {
	lanes := make(map[LaneID]struct{})
	for _, ep := range [2]*Epoch{w.Current, w.Next} {
		for lane := range ep.Committee().Lanes().All() {
			lanes[lane] = struct{}{}
		}
	}
	return lanes
}

// AllLanes returns the union of lanes across all three epochs (Prev, Current, Next).
// Used when decommissioning lanes: Prev-epoch lanes must be retained until any
// boundary QC that spans the epoch transition has been fully collected.
func (w EpochTrio) AllLanes() map[LaneID]struct{} {
	lanes := make(map[LaneID]struct{})
	for _, oep := range w.all() {
		if ep, ok := oep.Get(); ok {
			for lane := range ep.Committee().Lanes().All() {
				lanes[lane] = struct{}{}
			}
		}
	}
	return lanes
}

// VerifyInWindow calls fn against Current and Next only, skipping Prev.
// Use for votes and blocks, which must belong to the current or upcoming epoch.
func (w EpochTrio) VerifyInWindow(fn func(*Committee) error) (*Epoch, error) {
	for _, ep := range [2]*Epoch{w.Current, w.Next} {
		if fn(ep.Committee()) == nil {
			return ep, nil
		}
	}
	return nil, fmt.Errorf("not accepted by current or next epoch in %v", w)
}

// String returns a compact description of the epoch indices in the window.
func (w EpochTrio) String() string {
	s := "epochs ["
	sep := ""
	for _, oep := range w.all() {
		if ep, ok := oep.Get(); ok {
			s += fmt.Sprintf("%s%d", sep, ep.EpochIndex())
			sep = ", "
		}
	}
	return s + "]"
}
