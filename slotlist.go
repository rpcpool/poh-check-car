package main

import "fmt"

// slotList checks that the CAR's blocks are exactly the RPC's finalized slots, in order.
// PoH does not bind Alpenglow slot numbers, so this catches missing, extra or relabeled blocks.
type slotList struct {
	want []uint64
	next int
}

func newSlotList(want []uint64) (*slotList, error) {
	for i := 1; i < len(want); i++ {
		if want[i] <= want[i-1] {
			return nil, fmt.Errorf("RPC slot list is not ascending: %d after %d", want[i], want[i-1])
		}
	}
	return &slotList{want: want}, nil
}

// see checks the next block in the CAR. A nil list accepts every slot.
func (l *slotList) see(slot uint64) error {
	if l == nil {
		return nil
	}
	if l.next >= len(l.want) {
		return fmt.Errorf("slot %d is in the CAR, but the RPC lists no more blocks in the range", slot)
	}
	want := l.want[l.next]
	if slot < want {
		return fmt.Errorf("slot %d is in the CAR, but the RPC has no block there (next RPC block is %d)", slot, want)
	}
	if slot > want {
		return fmt.Errorf("the RPC has a block at slot %d, but the CAR skips to %d", want, slot)
	}
	l.next++
	return nil
}

// finish checks that the CAR held every slot the RPC lists.
func (l *slotList) finish() error {
	if l == nil || l.next == len(l.want) {
		return nil
	}
	return fmt.Errorf("the CAR ends before slot %d: the RPC lists %d more blocks in the range", l.want[l.next], len(l.want)-l.next)
}
