package main

import (
	"bytes"
	"fmt"

	"github.com/gagliardetto/solana-go"
	"github.com/rpcpool/yellowstone-faithful/blockmarker"
	"github.com/rpcpool/yellowstone-faithful/ipld/ipldbindcode"
)

// TowerBFT PoH: 64 ticks of 12,500 hashes per slot, skipped slots included.
const towerHashesPerSlot = 64 * 12_500

// blockSignal is sent through the ordered worker pipeline once a block's
// entries have been queued (in a CAR, a block comes after its entries).
type blockSignal struct {
	Slot       uint64
	ParentSlot uint64
	Alpenglow  bool
}

// blockChecker checks Alpenglow block markers and the block-id chain, one block at a time in CAR order.
// Agave treats every slot after the genesis certificate's slot as Alpenglow, and Alpenglow blocks
// must carry a parent marker (header or UpdateParent) and a footer.
type blockChecker struct {
	genesisSlot    OptionalUint64 // from --alpenglow-genesis-slot or a genesis certificate marker
	genesisBlockID *[32]byte      // from a genesis certificate marker
	lastTowerSlot  OptionalUint64
	sawAlpenglow   bool
	prevBlockID    []byte // nil after a TowerBFT block, which has no block id in the CAR
}

// check validates one block and reports whether it follows Alpenglow rules.
func (c *blockChecker) check(block *ipldbindcode.Block) (bool, error) {
	slot := uint64(block.Slot)
	markers, _ := block.GetBlockMarkers()
	blockID, hasBlockID := block.GetBlockID()

	if len(markers) == 0 {
		if hasBlockID {
			return false, fmt.Errorf("slot %d: block id without block markers", slot)
		}
		if c.sawAlpenglow {
			return false, fmt.Errorf("slot %d: TowerBFT block after an Alpenglow block", slot)
		}
		if c.genesisSlot.IsSet() && slot > c.genesisSlot.Get() {
			return false, fmt.Errorf("slot %d: no block markers, but slot is after Alpenglow genesis slot %d", slot, c.genesisSlot.Get())
		}
		c.lastTowerSlot.SetValue(slot)
		c.prevBlockID = nil
		return false, nil
	}

	if !hasBlockID || len(blockID) != 32 {
		return false, fmt.Errorf("slot %d: Alpenglow block without a 32-byte block id", slot)
	}

	var (
		hasFooter     bool
		hasParent     bool
		parentSlot    uint64
		parentBlockID [32]byte
	)
	for i, raw := range markers {
		m, err := blockmarker.Parse(raw)
		if err != nil {
			return false, fmt.Errorf("slot %d: marker %d: %w", slot, i, err)
		}
		switch m.Variant {
		case blockmarker.VariantFooter:
			if hasFooter {
				return false, fmt.Errorf("slot %d: more than one footer", slot)
			}
			if _, err := m.Footer(); err != nil {
				return false, fmt.Errorf("slot %d: %w", slot, err)
			}
			hasFooter = true
		case blockmarker.VariantHeader:
			h, err := m.Header()
			if err != nil {
				return false, fmt.Errorf("slot %d: %w", slot, err)
			}
			hasParent, parentSlot, parentBlockID = true, h.ParentSlot, h.ParentBlockID
		case blockmarker.VariantUpdateParent:
			// Fast leader handover: the later parent wins.
			u, err := m.UpdateParent()
			if err != nil {
				return false, fmt.Errorf("slot %d: %w", slot, err)
			}
			hasParent, parentSlot, parentBlockID = true, u.NewParentSlot, u.NewParentBlockID
		case blockmarker.VariantGenesisCertificate:
			g, err := m.GenesisCert()
			if err != nil {
				return false, fmt.Errorf("slot %d: %w", slot, err)
			}
			if err := c.setGenesis(g); err != nil {
				return false, fmt.Errorf("slot %d: %w", slot, err)
			}
		}
	}
	if !hasFooter {
		return false, fmt.Errorf("slot %d: Alpenglow block without a footer", slot)
	}
	if !hasParent {
		return false, fmt.Errorf("slot %d: Alpenglow block without a header or UpdateParent marker", slot)
	}
	if parentSlot != uint64(block.Meta.Parent_slot) {
		return false, fmt.Errorf("slot %d: parent marker names slot %d, block meta names %d", slot, parentSlot, block.Meta.Parent_slot)
	}
	if c.genesisSlot.IsSet() && slot <= c.genesisSlot.Get() {
		return false, fmt.Errorf("slot %d: block markers at or before Alpenglow genesis slot %d", slot, c.genesisSlot.Get())
	}

	// The previous block's id is known if it was an Alpenglow block, or it is the genesis block.
	var wantParentID []byte
	if c.prevBlockID != nil {
		wantParentID = c.prevBlockID
	} else if c.genesisBlockID != nil && parentSlot == c.genesisSlot.Get() {
		wantParentID = c.genesisBlockID[:]
	}
	if wantParentID != nil && !bytes.Equal(wantParentID, parentBlockID[:]) {
		return false, fmt.Errorf(
			"slot %d: parent block id %s does not match block %d's id %s",
			slot, solana.Hash(parentBlockID), parentSlot, solana.HashFromBytes(wantParentID),
		)
	}

	c.sawAlpenglow = true
	c.prevBlockID = append([]byte(nil), blockID...)
	return true, nil
}

func (c *blockChecker) setGenesis(g *blockmarker.GenesisCertificate) error {
	if c.genesisSlot.IsSet() && c.genesisSlot.Get() != g.Slot {
		return fmt.Errorf("genesis certificate is for slot %d, but Alpenglow genesis slot is %d", g.Slot, c.genesisSlot.Get())
	}
	if c.lastTowerSlot.IsSet() && c.lastTowerSlot.Get() > g.Slot {
		return fmt.Errorf("genesis certificate is for slot %d, but TowerBFT block %d came after it", g.Slot, c.lastTowerSlot.Get())
	}
	c.genesisSlot.SetValue(g.Slot)
	id := g.BlockID
	c.genesisBlockID = &id
	return nil
}

// blockEntryStats collects what the PoH rules need to know about one block's entries.
type blockEntryStats struct {
	numEntries      int
	numTicks        int
	lastIsTick      bool
	firstBadHashIdx int // first entry with num_hashes != 1, or -1
	numHashes       uint64
}

func newBlockEntryStats() blockEntryStats {
	return blockEntryStats{firstBadHashIdx: -1}
}

func (s *blockEntryStats) add(entry *ipldbindcode.Entry) {
	isTick := len(entry.Transactions) == 0
	if entry.NumHashes != 1 && s.firstBadHashIdx < 0 {
		s.firstBadHashIdx = s.numEntries
	}
	if isTick {
		s.numTicks++
	}
	s.lastIsTick = isTick
	s.numEntries++
	s.numHashes += uint64(entry.NumHashes)
}

// checkAlpenglow applies agave's low-power PoH rule: every entry does one hash,
// and the only tick is the alpentick at the end of the block.
func (s *blockEntryStats) checkAlpenglow(slot uint64) error {
	switch {
	case s.numEntries == 0:
		return fmt.Errorf("slot %d: Alpenglow block has no entries", slot)
	case s.firstBadHashIdx >= 0:
		return fmt.Errorf("slot %d: Alpenglow entry %d does not have num_hashes == 1", slot, s.firstBadHashIdx)
	case !s.lastIsTick:
		return fmt.Errorf("slot %d: Alpenglow block does not end with a tick", slot)
	case s.numTicks != 1:
		return fmt.Errorf("slot %d: Alpenglow block has %d ticks, want 1", slot, s.numTicks)
	}
	return nil
}
