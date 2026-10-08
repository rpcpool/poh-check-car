package main

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"

	"github.com/ipld/go-ipld-prime/datamodel"
	"github.com/rpcpool/yellowstone-faithful/ipld/ipldbindcode"
)

func rawMarker(variant uint8, payload []byte) []byte {
	b := binary.LittleEndian.AppendUint16(nil, 1)
	b = append(b, variant)
	b = binary.LittleEndian.AppendUint16(b, uint16(len(payload)))
	return append(b, payload...)
}

func id(b byte) []byte { return bytes.Repeat([]byte{b}, 32) }

func footerMarker() []byte {
	p := append([]byte{1}, id(0xab)...)
	p = binary.LittleEndian.AppendUint64(p, 1_700_000_000)
	p = append(p, 0) // empty user agent, no certificates
	return rawMarker(0, p)
}

func parentMarker(variant uint8, slot uint64, blockID []byte) []byte {
	p := binary.LittleEndian.AppendUint64([]byte{1}, slot)
	return rawMarker(variant, append(p, blockID...))
}

func headerMarker(slot uint64, blockID []byte) []byte       { return parentMarker(1, slot, blockID) }
func updateParentMarker(slot uint64, blockID []byte) []byte { return parentMarker(2, slot, blockID) }

func genesisCertMarker(slot uint64, blockID []byte) []byte {
	p := binary.LittleEndian.AppendUint64(nil, slot)
	p = append(p, blockID...)
	p = append(p, make([]byte, 192)...)
	p = binary.LittleEndian.AppendUint64(p, 0) // empty bitmap
	return rawMarker(3, p)
}

func towerBlock(slot, parent int) *ipldbindcode.Block {
	b := &ipldbindcode.Block{Slot: slot}
	b.Meta.Parent_slot = parent
	return b
}

func alpenglowBlock(slot, parent int, blockID []byte, markers ...[]byte) *ipldbindcode.Block {
	b := towerBlock(slot, parent)
	list := ipldbindcode.List__Bytes(markers)
	pList := &list
	b.Meta.Block_markers = &pList
	pID := &blockID
	b.Meta.Block_id = &pID
	return b
}

func runBlocks(c *blockChecker, blocks ...*ipldbindcode.Block) error {
	for _, b := range blocks {
		if _, err := c.check(b); err != nil {
			return err
		}
	}
	return nil
}

func TestBlockCheckerMigration(t *testing.T) {
	c := &blockChecker{}
	err := runBlocks(c,
		towerBlock(100, 99),
		alpenglowBlock(101, 100, id(1), headerMarker(100, id(0x99)), genesisCertMarker(100, id(0x99)), footerMarker()),
		alpenglowBlock(102, 101, id(2), headerMarker(101, id(1)), footerMarker()),
		// Fast leader handover: the UpdateParent marker replaces the header's parent.
		alpenglowBlock(104, 102, id(4), headerMarker(103, id(3)), updateParentMarker(102, id(2)), footerMarker()),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !c.genesisSlot.IsSet() || c.genesisSlot.Get() != 100 {
		t.Fatalf("genesis slot not taken from the certificate: %v", c.genesisSlot)
	}
}

func TestBlockCheckerErrors(t *testing.T) {
	genesis := OptionalUint64{}
	genesis.SetValue(100)
	tests := []struct {
		name    string
		checker *blockChecker
		blocks  []*ipldbindcode.Block
		wantErr string
	}{
		{
			name:    "missing footer",
			checker: &blockChecker{},
			blocks:  []*ipldbindcode.Block{alpenglowBlock(101, 100, id(1), headerMarker(100, id(0)))},
			wantErr: "without a footer",
		},
		{
			name:    "missing parent marker",
			checker: &blockChecker{},
			blocks:  []*ipldbindcode.Block{alpenglowBlock(101, 100, id(1), footerMarker())},
			wantErr: "without a header or UpdateParent",
		},
		{
			name:    "missing block id",
			checker: &blockChecker{},
			blocks:  []*ipldbindcode.Block{alpenglowBlock(101, 100, nil, headerMarker(100, id(0)), footerMarker())},
			wantErr: "32-byte block id",
		},
		{
			name:    "parent marker disagrees with meta",
			checker: &blockChecker{},
			blocks:  []*ipldbindcode.Block{alpenglowBlock(101, 100, id(1), headerMarker(99, id(0)), footerMarker())},
			wantErr: "parent marker names slot 99",
		},
		{
			name:    "broken block id chain",
			checker: &blockChecker{},
			blocks: []*ipldbindcode.Block{
				alpenglowBlock(101, 100, id(1), headerMarker(100, id(0)), footerMarker()),
				alpenglowBlock(102, 101, id(2), headerMarker(101, id(7)), footerMarker()),
			},
			wantErr: "parent block id",
		},
		{
			name:    "genesis block id mismatch",
			checker: &blockChecker{},
			blocks: []*ipldbindcode.Block{
				alpenglowBlock(101, 100, id(1), headerMarker(100, id(0x98)), genesisCertMarker(100, id(0x99)), footerMarker()),
			},
			wantErr: "parent block id",
		},
		{
			name:    "TowerBFT after Alpenglow",
			checker: &blockChecker{},
			blocks: []*ipldbindcode.Block{
				alpenglowBlock(101, 100, id(1), headerMarker(100, id(0)), footerMarker()),
				towerBlock(102, 101),
			},
			wantErr: "TowerBFT block after an Alpenglow block",
		},
		{
			name:    "TowerBFT after genesis slot flag",
			checker: &blockChecker{genesisSlot: genesis},
			blocks:  []*ipldbindcode.Block{towerBlock(101, 100)},
			wantErr: "after Alpenglow genesis slot 100",
		},
		{
			name:    "Alpenglow at genesis slot flag",
			checker: &blockChecker{genesisSlot: genesis},
			blocks:  []*ipldbindcode.Block{alpenglowBlock(100, 99, id(1), headerMarker(99, id(0)), footerMarker())},
			wantErr: "at or before Alpenglow genesis slot 100",
		},
		{
			name:    "certificate disagrees with genesis slot flag",
			checker: &blockChecker{genesisSlot: genesis},
			blocks: []*ipldbindcode.Block{
				alpenglowBlock(102, 101, id(1), headerMarker(101, id(0)), genesisCertMarker(101, id(0)), footerMarker()),
			},
			wantErr: "genesis certificate is for slot 101",
		},
		{
			name:    "TowerBFT block after certificate slot",
			checker: &blockChecker{},
			blocks: []*ipldbindcode.Block{
				towerBlock(101, 100),
				alpenglowBlock(102, 101, id(1), headerMarker(101, id(0)), genesisCertMarker(100, id(0)), footerMarker()),
			},
			wantErr: "TowerBFT block 101 came after it",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := runBlocks(tt.checker, tt.blocks...)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("got error %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func entry(numHashes, numTxs int) *ipldbindcode.Entry {
	return &ipldbindcode.Entry{NumHashes: numHashes, Transactions: make([]datamodel.Link, numTxs)}
}

func TestBlockEntryStatsAlpenglow(t *testing.T) {
	tests := []struct {
		name    string
		entries []*ipldbindcode.Entry
		wantErr string
	}{
		{name: "valid", entries: []*ipldbindcode.Entry{entry(1, 3), entry(1, 1), entry(1, 0)}},
		{name: "only alpentick", entries: []*ipldbindcode.Entry{entry(1, 0)}},
		{name: "no entries", wantErr: "no entries"},
		{name: "extra hashes", entries: []*ipldbindcode.Entry{entry(1, 3), entry(5, 1), entry(1, 0)}, wantErr: "entry 1 does not have num_hashes == 1"},
		{name: "no trailing tick", entries: []*ipldbindcode.Entry{entry(1, 3)}, wantErr: "does not end with a tick"},
		{name: "two ticks", entries: []*ipldbindcode.Entry{entry(1, 0), entry(1, 2), entry(1, 0)}, wantErr: "has 2 ticks"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newBlockEntryStats()
			for _, e := range tt.entries {
				s.add(e)
			}
			err := s.checkAlpenglow(1)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("got error %v, want %q", err, tt.wantErr)
			}
		})
	}
}

// towerSlot returns the entries of a TowerBFT slot: a transaction entry and a tick per tick.
func towerSlot(numTicks, hashesPerTick int) []*ipldbindcode.Entry {
	var out []*ipldbindcode.Entry
	for i := 0; i < numTicks; i++ {
		out = append(out, entry(1, 2), entry(hashesPerTick-1, 0))
	}
	return out
}

func TestBlockEntryStatsTower(t *testing.T) {
	tests := []struct {
		name          string
		slot, parent  uint64
		entries       []*ipldbindcode.Entry
		hashesPerTick uint64
		wantErr       string
	}{
		{name: "valid", slot: 10, parent: 9, entries: towerSlot(64, 100)},
		{name: "skipped slots", slot: 12, parent: 9, entries: towerSlot(192, 100), hashesPerTick: 100},
		{name: "too few ticks", slot: 12, parent: 9, entries: towerSlot(64, 100), wantErr: "64 ticks, want 192"},
		{name: "trailing entry", slot: 10, parent: 9, entries: append(towerSlot(64, 100), entry(1, 1)), wantErr: "does not end with a tick"},
		{name: "uneven ticks", slot: 10, parent: 9, entries: append(towerSlot(63, 100), entry(5, 0)), wantErr: "different hash counts"},
		{name: "hashes per tick changed", slot: 10, parent: 9, entries: towerSlot(64, 100), hashesPerTick: 39062, wantErr: "ticks have 100 hashes, earlier blocks had 39062"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newBlockEntryStats()
			for _, e := range tt.entries {
				s.add(e)
			}
			hpt := tt.hashesPerTick
			err := s.checkTower(tt.slot, tt.parent, &hpt)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				if hpt != 100 {
					t.Fatalf("hashes per tick = %d, want 100", hpt)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("got error %v, want %q", err, tt.wantErr)
			}
		})
	}
}
