package types

import (
	"fmt"

	"github.com/sei-protocol/sei-chain/sei-tendermint/libs/utils"
)

// EpochTrio is a sliding window of up to three consecutive epochs.
// Current and Next are always set; Prev is absent only for epoch 0.
type EpochTrio struct {
	Prev    utils.Option[*Epoch] // absent if Current is epoch 0
	Current *Epoch
	Next    *Epoch
}

// all is Current, Next, Prev — EpochForRoad prefers Current so an open-range
// Prev cannot shadow later epochs.
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

func (w EpochTrio) CurrentAndNextLanes() map[LaneID]struct{} {
	lanes := make(map[LaneID]struct{})
	for _, ep := range [2]*Epoch{w.Current, w.Next} {
		for lane := range ep.Committee().Lanes().All() {
			lanes[lane] = struct{}{}
		}
	}
	return lanes
}

// VerifyInWindow tries fn against Current then Next (not Prev).
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
