package main

import (
	"context"
	"flag"
	"fmt"
	"strconv"
	"strings"

	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc"
)

type Helper struct {
	epoch     uint64
	ctx       context.Context
	rpcClient *rpc.Client
}

func NewHelper(epoch uint64, rpcClient *rpc.Client) *Helper {
	return &Helper{
		epoch:     epoch,
		ctx:       context.Background(),
		rpcClient: rpcClient,
	}
}

// WithContext(ctx context.Context) *Helper
func (h *Helper) WithContext(ctx context.Context) *Helper {
	h.ctx = ctx
	return h
}

// GetBlocks(start,end uint64) ([]uint64, error)
func (h *Helper) GetBlocks(start, end uint64) (rpc.BlocksResult, error) {
	blocks, err := retryExponentialBackoff(DefaultRetries, func() (rpc.BlocksResult, error) {
		return h.rpcClient.GetBlocks(
			h.ctx,
			start,
			&end,
			rpc.CommitmentConfirmed,
		)
	})
	if err != nil {
		return nil, err
	}
	return blocks, nil
}

func (h *Helper) GetFirstProducedBlock(epoch uint64) (uint64, error) {
	epochStartSlot := uint64(epoch * EpochLen)

	// first try with epochEndSlot + 1000, then with epochEndSlot + 2000, etc.
	// until we find a block.
	maxTries := 10
	increment := 1000
	for i := 1; i < maxTries; i++ {
		epochEndSlot := epochStartSlot + uint64(i*increment)
		blocks, err := h.GetBlocks(epochStartSlot, epochEndSlot)
		if err != nil {
			return 0, fmt.Errorf("failed to get blocks for epoch %d: %s", epoch, err)
		}
		if len(blocks) == 0 {
			continue
		}
		return blocks[0], nil
	}
	return 0, fmt.Errorf("could not find the first block for epoch %d between slots %d and %d", epoch, epochStartSlot, epochStartSlot+uint64(maxTries*increment))
}

var (
	TRUE  = true
	FALSE = false
)

// GetBlock(block uint64) (*rpc.BlockResult, error)
func (h *Helper) GetBlock(slot uint64) (*rpc.GetBlockResult, error) {
	block, err := retryExponentialBackoff(DefaultRetries, func() (*rpc.GetBlockResult, error) {
		return h.rpcClient.GetBlockWithOpts(
			h.ctx,
			slot,
			&rpc.GetBlockOpts{
				Encoding:           solana.EncodingBase64,
				Commitment:         rpc.CommitmentConfirmed,
				TransactionDetails: rpc.TransactionDetailsNone,
				Rewards:            &FALSE,
			},
		)
	})
	if err != nil {
		return nil, err
	}
	return block, nil
}

// GetGenesisHash() (string, error)
func (h *Helper) GetGenesisHash() (solana.Hash, error) {
	return retryExponentialBackoff(DefaultRetries, func() (solana.Hash, error) {
		return h.rpcClient.GetGenesisHash(
			h.ctx,
		)
	})
}

type EpochLimits struct {
	Epoch uint64

	PreviousBlockSlot uint64 // The slot of the last block of the previous epoch.
	PreviousBlockhash solana.Hash

	FirstBlockSlot uint64 // The slot of the first block of the current epoch.
	FirstBlockhash solana.Hash

	LastBlockSlot uint64 // The slot of the last block of the current epoch.
	LastBlockhash solana.Hash

	NextBlockSlot uint64 // The slot of the first block of the next epoch.
	NextBlockhash solana.Hash

	StartSlot OptionalUint64 // The slot from which to start poh verification.
	EndSlot   OptionalUint64 // The slot at which to end poh verification.
}

// GetActualStartStopSlots() (start, stop uint64)
func (el *EpochLimits) GetActualStartStopSlots() (start, stop uint64) {
	if el.StartSlot.IsSet() {
		start = el.StartSlot.Get()
	} else {
		start = el.FirstBlockSlot
	}
	if el.EndSlot.IsSet() {
		stop = el.EndSlot.Get()
	} else {
		stop = el.LastBlockSlot
	}
	if start > stop {
		panic(fmt.Sprintf("start slot %d is greater than stop slot %d", start, stop))
	}
	return start, stop
}

type OptionalUint64 struct {
	Value uint64
	isSet bool
}

// implement the flag.Value interface
func (ou *OptionalUint64) String() string {
	if ou == nil {
		return "nil"
	}
	if !ou.isSet {
		return "unset"
	}
	return fmt.Sprintf("%d", ou.Value)
}

func (ou *OptionalUint64) Set(value string) error {
	if ou == nil {
		ou = &OptionalUint64{}
	}
	v, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return fmt.Errorf("failed to parse value %s: %s", value, err)
	}
	ou.Value = v
	ou.isSet = true
	return nil
}

func (ou *OptionalUint64) SetValue(value uint64) {
	if ou == nil {
		ou = &OptionalUint64{}
	}
	ou.Value = value
	ou.isSet = true
}

func (ou *OptionalUint64) IsSet() bool {
	if ou == nil {
		return false
	}
	return ou.isSet
}

func (ou *OptionalUint64) Get() uint64 {
	if ou == nil {
		panic("OptionalUint64 is nil")
	}
	return ou.Value
}

func (ou *OptionalUint64) Unset() {
	if ou == nil {
		ou = &OptionalUint64{}
	}
	ou.Value = 0
	ou.isSet = false
}

// AssertPreviousBlockSlot(candidate uint64) error
func (el *EpochLimits) AssertPreviousBlockSlot(got uint64) error {
	if el.PreviousBlockSlot != got {
		return fmt.Errorf("expected the last block of the previous epoch %d to be %d, but got %d", el.Epoch-1, el.PreviousBlockSlot, got)
	}
	return nil
}

// AssertPreviousBlockhash(candidate solana.Hash) error
func (el *EpochLimits) AssertPreviousBlockhash(got solana.Hash) error {
	if el.PreviousBlockhash != got {
		return fmt.Errorf("expected the last block of the previous epoch %d to have hash %s, but got %s", el.Epoch-1, el.PreviousBlockhash, got)
	}
	return nil
}

// AssertFirstBlockSlot(candidate uint64) error
func (el *EpochLimits) AssertFirstBlockSlot(got uint64) error {
	if el.FirstBlockSlot != got {
		return fmt.Errorf("expected the first block of the epoch %d to be %d, but got %d", el.Epoch, el.FirstBlockSlot, got)
	}
	return nil
}

// AssertFirstBlockhash(candidate solana.Hash) error
func (el *EpochLimits) AssertFirstBlockhash(got solana.Hash) error {
	if el.FirstBlockhash != got {
		return fmt.Errorf("expected the first block of the epoch %d to have hash %s, but got %s", el.Epoch, el.FirstBlockhash, got)
	}
	return nil
}

// AssertLastBlockSlot(candidate uint64) error
func (el *EpochLimits) AssertLastBlockSlot(got uint64) error {
	if el.LastBlockSlot != got {
		return fmt.Errorf("expected the last block of the epoch %d to be %d, but got %d", el.Epoch, el.LastBlockSlot, got)
	}
	return nil
}

// AssertLastBlockhash(candidate solana.Hash) error
func (el *EpochLimits) AssertLastBlockhash(got solana.Hash) error {
	if el.LastBlockhash != got {
		return fmt.Errorf("expected the last block of the epoch %d to have hash %s, but got %s", el.Epoch, el.LastBlockhash, got)
	}
	return nil
}

// AssertNextBlockSlot(candidate uint64) error
func (el *EpochLimits) AssertNextBlockSlot(got uint64) error {
	if el.NextBlockSlot != got {
		return fmt.Errorf("expected the first block of the next epoch %d to be %d, but got %d", el.Epoch+1, el.NextBlockSlot, got)
	}
	return nil
}

// AssertNextBlockhash(candidate solana.Hash) error
func (el *EpochLimits) AssertNextBlockhash(got solana.Hash) error {
	if el.NextBlockhash != got {
		return fmt.Errorf("expected the first block of the next epoch %d to have hash %s, but got %s", el.Epoch+1, el.NextBlockhash, got)
	}
	return nil
}

func (el *EpochLimits) String() string {
	paddingLen := len(fmt.Sprintf("%d(%s)", el.LastBlockSlot, el.LastBlockhash)) + 2
	buf := new(strings.Builder)
	if el.Epoch == 0 {
		buf.WriteString(fmt.Sprintf("NO prev epoch; genesis hash: %s\n", el.PreviousBlockhash))
	} else {
		buf.WriteString(fmt.Sprintf(
			"prev epoch(%d):%s... %d(%s)\n",
			CalcEpochForSlot(el.PreviousBlockSlot),
			strings.Repeat(" ", paddingLen),
			el.PreviousBlockSlot,
			el.PreviousBlockhash,
		))
	}
	buf.WriteString(fmt.Sprintf(
		"THIS epoch(%d): %d(%s) ... %d(%s)\n",
		CalcEpochForSlot(el.FirstBlockSlot),
		el.FirstBlockSlot,
		el.FirstBlockhash,
		el.LastBlockSlot,
		el.LastBlockhash,
	))
	buf.WriteString(fmt.Sprintf(
		"next epoch(%d): %d(%s) ...\n",
		CalcEpochForSlot(el.NextBlockSlot),
		el.NextBlockSlot,
		el.NextBlockhash,
	))
	return buf.String()
}

func (el *EpochLimits) PrintAssertions() {
	if el.PreviousBlockSlot != 0 {
		fmt.Printf("- Will assert previous block slot to be %d\n", el.PreviousBlockSlot)
	}
	if !el.PreviousBlockhash.IsZero() {
		msg := fmt.Sprintf("- Will assert previous blockhash to be %s", el.PreviousBlockhash)
		if el.Epoch == 0 {
			msg += " (genesis hash)"
		}
		fmt.Println(msg)
	}

	if el.FirstBlockSlot != 0 || el.FirstBlockSlot == 0 && el.Epoch == 0 {
		fmt.Printf("- Will assert first block slot to be %d\n", el.FirstBlockSlot)
	}
	if !el.FirstBlockhash.IsZero() {
		fmt.Printf("- Will assert first blockhash to be %s\n", el.FirstBlockhash)
	}

	if el.LastBlockSlot != 0 {
		fmt.Printf("- Will assert last block slot to be %d\n", el.LastBlockSlot)
	}
	if !el.LastBlockhash.IsZero() {
		fmt.Printf("- Will assert last blockhash to be %s\n", el.LastBlockhash)
	}

	// TODO:
	// if el.NextBlockSlot != 0 {
	// 	fmt.Printf("- Will assert next epoch's first block slot to be %d\n", el.NextBlockSlot)
	// }
	// if !el.NextBlockhash.IsZero() {
	// 	fmt.Printf("- Will assert next epoch's first blockhash to be %s\n", el.NextBlockhash)
	// }
}

type LimitFlags struct {
	PreviousBlockSlot uint64
	PreviousBlockhash string

	FirstBlockSlot uint64
	FirstBlockhash string

	LastBlockSlot uint64
	LastBlockhash string

	NextBlockSlot uint64
	NextBlockhash string

	StartSlot OptionalUint64
	EndSlot   OptionalUint64
}

func (el *LimitFlags) AddToFlagSet(fs *flag.FlagSet) {
	fs.Uint64Var(&el.PreviousBlockSlot, "prev-slot", 0, "The slot of the last block of the previous epoch.")
	fs.StringVar(&el.PreviousBlockhash, "prev-hash", "", "The hash of the last block of the previous epoch.")

	fs.Uint64Var(&el.FirstBlockSlot, "first-slot", 0, "The slot of the first block of the current epoch.")
	fs.StringVar(&el.FirstBlockhash, "first-hash", "", "The hash of the first block of the current epoch.")

	fs.Uint64Var(&el.LastBlockSlot, "last-slot", 0, "The slot of the last block of the current epoch.")
	fs.StringVar(&el.LastBlockhash, "last-hash", "", "The hash of the last block of the current epoch.")

	fs.Uint64Var(&el.NextBlockSlot, "next-slot", 0, "The slot of the first block of the next epoch.")
	fs.StringVar(&el.NextBlockhash, "next-hash", "", "The hash of the first block of the next epoch.")

	fs.Var(&el.StartSlot, "start", "The slot from which to start poh verification.")
	fs.Var(&el.EndSlot, "end", "The slot at which to end poh verification.")
}

func isFlagPassed(name string, fs *flag.FlagSet) bool {
	found := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

func (el *EpochLimits) ApplyOverrides(lfs *LimitFlags, fs *flag.FlagSet) error {
	{
		if isFlagPassed("prev-slot", fs) {
			fmt.Printf("Overriding previous block slot with %d\n", lfs.PreviousBlockSlot)
			el.PreviousBlockSlot = lfs.PreviousBlockSlot
		}

		if isFlagPassed("prev-hash", fs) {
			fmt.Printf("Overriding previous block hash with %s\n", lfs.PreviousBlockhash)
			el.PreviousBlockhash = solana.MustHashFromBase58(lfs.PreviousBlockhash)
		}
	}

	{
		if isFlagPassed("first-slot", fs) {
			fmt.Printf("Overriding first block slot with %d\n", lfs.FirstBlockSlot)
			el.FirstBlockSlot = lfs.FirstBlockSlot
		}

		if isFlagPassed("first-hash", fs) {
			fmt.Printf("Overriding first block hash with %s\n", lfs.FirstBlockhash)
			el.FirstBlockhash = solana.MustHashFromBase58(lfs.FirstBlockhash)
		}
	}

	{
		if isFlagPassed("last-slot", fs) {
			fmt.Printf("Overriding last block slot with %d\n", lfs.LastBlockSlot)
			el.LastBlockSlot = lfs.LastBlockSlot
		}

		if isFlagPassed("last-hash", fs) {
			fmt.Printf("Overriding last block hash with %s\n", lfs.LastBlockhash)
			el.LastBlockhash = solana.MustHashFromBase58(lfs.LastBlockhash)
		}
	}

	{
		if isFlagPassed("next-slot", fs) {
			fmt.Printf("Overriding next block slot with %d\n", lfs.NextBlockSlot)
			el.NextBlockSlot = lfs.NextBlockSlot
		}

		if isFlagPassed("next-hash", fs) {
			fmt.Printf("Overriding next block hash with %s\n", lfs.NextBlockhash)
			el.NextBlockhash = solana.MustHashFromBase58(lfs.NextBlockhash)
		}
	}
	{
		epochStart, epochEnd := CalcEpochLimits(uint64(el.Epoch))
		if isFlagPassed("start", fs) && lfs.StartSlot.Get() != epochStart {
			fmt.Printf("Overriding start slot with %d\n", lfs.StartSlot.Get())
			el.StartSlot.SetValue(uint64(lfs.StartSlot.Get()))
			el.FirstBlockSlot = lfs.StartSlot.Get() // We are overriding the first block slot to be the start slot.
		} else {
			el.StartSlot.Unset()
		}
		if isFlagPassed("end", fs) && lfs.EndSlot.Get() != epochEnd {
			fmt.Printf("Overriding end slot with %d\n", lfs.EndSlot.Get())
			// TODO: check if start is set.
			if lfs.EndSlot.Get() < (el.StartSlot.Get()) {
				return fmt.Errorf("end slot must be greater than or equal to start slot")
			}
			el.EndSlot = lfs.EndSlot
			el.LastBlockSlot = (lfs.EndSlot.Get()) // We are overriding the last block slot to be the end slot.
		} else {
			el.EndSlot.Unset()
		}
	}

	return nil
}

func (el *EpochLimits) isCustomRange() bool {
	if el.StartSlot.IsSet() || el.EndSlot.IsSet() {
		return true
	}
	return false
}

// GetEpochLimits(epoch uint64) (*EpochLimits, error)
func (h *Helper) GetEpochLimits() (*EpochLimits, error) {
	epochNum := h.epoch

	limits := &EpochLimits{
		Epoch: epochNum,
	}
	{
		firstBlockSlot, err := h.GetFirstProducedBlock(epochNum)
		if err != nil {
			return nil, fmt.Errorf("failed to get first available block for epoch %d: %s", epochNum, err)
		}

		limits.FirstBlockSlot = firstBlockSlot

		firstBlock, err := h.GetBlock(firstBlockSlot)
		if err != nil {
			return nil, fmt.Errorf("failed to get first block %d for epoch %d: %s", firstBlockSlot, epochNum, err)
		}

		if epochNum == 0 {
			genesysHash, err := h.GetGenesisHash()
			if err != nil {
				return nil, fmt.Errorf("failed to get genesis hash: %s", err)
			}

			limits.FirstBlockhash = firstBlock.Blockhash
			limits.PreviousBlockSlot = 0
			limits.PreviousBlockhash = genesysHash
		} else {
			limits.FirstBlockhash = firstBlock.Blockhash
			limits.PreviousBlockSlot = firstBlock.ParentSlot
			limits.PreviousBlockhash = firstBlock.PreviousBlockhash
		}
	}

	nextEpochNum := epochNum + 1
	{
		nextEpochFirstBlock, err := h.GetFirstProducedBlock(nextEpochNum)
		if err != nil {
			return nil, fmt.Errorf("failed to get last available block for epoch %d: %s", epochNum, err)
		}

		block, err := h.GetBlock(nextEpochFirstBlock)
		if err != nil {
			return nil, fmt.Errorf("failed to get first block %d for epoch %d: %s", nextEpochFirstBlock, nextEpochNum, err)
		}

		limits.NextBlockSlot = nextEpochFirstBlock
		limits.NextBlockhash = block.Blockhash

		limits.LastBlockSlot = block.ParentSlot
		limits.LastBlockhash = block.PreviousBlockhash
	}

	return limits, nil
}
