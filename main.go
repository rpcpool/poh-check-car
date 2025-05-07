package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/davecgh/go-spew/spew"
	"github.com/dustin/go-humanize"
	bin "github.com/gagliardetto/binary"
	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc"
	blocks "github.com/ipfs/go-block-format"
	"github.com/ipld/go-car"
	"github.com/rpcpool/poh-check-car/merkletree"
	"github.com/rpcpool/poh-check-car/poh"
	"github.com/rpcpool/yellowstone-faithful/ipld/ipldbindcode"
	"github.com/rpcpool/yellowstone-faithful/iplddecoders"
	concurrently "github.com/tejzpr/ordered-concurrently/v3"
	"k8s.io/klog"
)

const (
	DefaultRetries = 5
)

func main() {
	var (
		carPath     string
		numWorkers  uint
		noProgress  bool
		epochNum    int64
		rpcEndpoint string
		limitFlags  = new(LimitFlags)
	)
	flag.StringVar(&carPath, "car", "", "Path to CAR file")
	flag.UintVar(&numWorkers, "workers", uint(runtime.NumCPU()), "Number of workers")
	flag.BoolVar(&noProgress, "no-progress", false, "Disable progress bar")
	flag.Int64Var(&epochNum, "epoch", -1, "Epoch number")
	flag.StringVar(&rpcEndpoint, "rpc", rpc.MainNetBeta.RPC, "RPC endpoint")
	limitFlags.AddToFlagSet(flag.CommandLine)
	flag.Parse()

	if isAnyOf(flag.Arg(0), "-v", "--version", "version", "v") {
		printVersion()
		return
	}

	if carPath == "" {
		klog.Exit("error: No CAR file given")
	}

	// check that the epoch number is set:
	if epochNum < 0 {
		klog.Exit("error: No epoch number given; please use the --epoch flag")
	}
	if rpcEndpoint == "" {
		klog.Exit("error: No RPC endpoint given")
	}
	if rpcEndpoint == rpc.MainNetBeta.RPC {
		klog.Infof("Using default RPC endpoint (slow and rate-limited): %s", rpcEndpoint)
	} else {
		klog.Infof("Using RPC endpoint: %s", rpcEndpoint)
	}
	helper := NewHelper(uint64(epochNum), rpc.New(rpcEndpoint))

	limits, err := helper.GetEpochLimits()
	if err != nil {
		klog.Exitf("error: failed to get epoch limits: %s", err)
	}
	fmt.Println("Epoch limits:")
	fmt.Println(limits.String())
	// spew.Config.DisableMethods = true
	// spew.Config.DisablePointerMethods = true
	// spew.Config.DisablePointerAddresses = true

	// spew.Dump(limits)
	limits.ApplyOverrides(limitFlags, flag.CommandLine)
	// spew.Dump(limits)
	if limits.isCustomRange() {
		// need to reset the blockhashes
		if limits.StartSlot.IsSet() {
			startBlock, err := helper.GetBlock((limits.StartSlot.Get()))
			if err != nil {
				klog.Exitf("error: failed to get block for start slot %d (you need to specify a slot that has a produced block): %s", limits.StartSlot.Get(), err)
			}
			limits.FirstBlockhash = startBlock.Blockhash // TODO: ????
			limits.PreviousBlockhash = startBlock.PreviousBlockhash

			limits.PreviousBlockSlot = startBlock.ParentSlot
		}

		if limits.EndSlot.IsSet() {
			endBlock, err := helper.GetBlock((limits.EndSlot.Get()))
			if err != nil {
				klog.Exitf("error: failed to get block for end slot %d (you need to specify a slot that has a produced block): %s", limits.EndSlot, err)
			}
			limits.LastBlockhash = endBlock.Blockhash
			limits.LastBlockSlot = limits.EndSlot.Get()
		}
		fmt.Println("PoH checking only for custom range:")
		fmt.Println(limits.String())
	}

	limits.PrintAssertions()

	startedAt := time.Now()
	defer func() {
		klog.Infof("Took %s", time.Since(startedAt))
	}()
	ctx := context.Background()
	if err := checkCar(
		ctx,
		carPath,
		numWorkers,
		noProgress,
		uint64(epochNum),
		limits,
	); err != nil {
		klog.Exitf("error: %s", err)
	}
	klog.Infof("Successfully checked PoH on CAR file for epoch %d", epochNum)
}

type entryCheckJob struct {
	Slot       uint64
	EntryIndex int
	Prev       poh.State
	Wanted     poh.State
	NumHashes  uint64
	numtx      int
	accu       [][]byte
}

func (j *entryCheckJob) assert() error {
	ha := poh.State(j.Prev)

	sigTree := merkletree.HashNodes(j.accu)

	root := sigTree.GetRoot()

	if root == nil {
		ha.Hash(uint(j.NumHashes))
	} else {
		if j.NumHashes > 1 {
			ha.Hash(uint(j.NumHashes - 1))
		}
		ha.Record(root)
	}
	if !bytes.Equal(j.Wanted[:], ha[:]) {
		return fmt.Errorf(
			"PoH mismatch for slot %d, entry %d: expected %s, actual %s",
			j.Slot,
			j.EntryIndex,
			solana.Hash(j.Wanted),
			solana.Hash(ha),
		)
	}
	return nil
}

func cloneAccumulator(acc [][]byte) [][]byte {
	out := make([][]byte, len(acc))
	for i := range acc {
		out[i] = make([]byte, len(acc[i]))
		copy(out[i], acc[i])
	}
	return out
}

func checkCar(
	ctx context.Context,
	carPath string,
	numWorkers uint,
	noProgress bool,
	epochNum uint64,
	limits *EpochLimits,
) error {
	file, err := os.Open(carPath)
	if err != nil {
		klog.Exitf("error: failed to open CAR file: %s", err)
	}
	defer file.Close()

	cachingReader := bufio.NewReaderSize(file, alignToPageSize(1024*1024*12))

	rd, err := car.NewCarReader(cachingReader)
	if err != nil {
		klog.Exitf("error: failed to create CAR reader: %s", err)
	}
	{
		// print roots:
		roots := rd.Header.Roots
		klog.Infof("Roots: %d", len(roots))
		for i, root := range roots {
			if i == 0 && len(roots) == 1 {
				klog.Infof("- %s (Epoch CID)", root.String())
			} else {
				klog.Infof("- %s", root.String())
			}
		}
	}

	startedAt := time.Now()
	numNodesSeen := 0
	numBlocksWhereCheckedPoH := 0
	defer func() {
		klog.Infof("Finished in %s", time.Since(startedAt))
		klog.Infof("Read %d nodes from CAR file", numNodesSeen)
		klog.Infof("Checked %d blocks for PoH", numBlocksWhereCheckedPoH)
	}()

	prevBlockHash := poh.State(limits.PreviousBlockhash)
	klog.Infof("epoch %d: prevBlockHash: %s", epochNum, solana.Hash(prevBlockHash))

	signatureAccumulator := make([][]byte, 0)

	if numWorkers == 0 {
		numWorkers = uint(runtime.NumCPU())
	}
	workerInputChan := make(chan concurrently.WorkFunction, numWorkers)
	waitExecuted := new(sync.WaitGroup)
	waitResultsReceived := new(sync.WaitGroup)
	numReceivedParsed := new(atomic.Int64)

	outputChan := concurrently.Process(
		context.Background(),
		workerInputChan,
		&concurrently.Options{PoolSize: int(numWorkers), OutChannelBuffer: int(numWorkers)},
	)

	wg := &sync.WaitGroup{}
	blockhash := solana.Hash([32]byte{})
	numCheckedEntries := new(atomic.Uint64)
	numHashes := new(atomic.Uint64)
	numBlocks := new(atomic.Uint64)

	// initialize a job channel that will be read by many workers:
	numJobProcessors := runtime.NumCPU() * 2
	jobChan := make(chan *entryCheckJob, numJobProcessors)
	// start the workers:
	for i := (0); i < numJobProcessors; i++ {
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case job, ok := <-jobChan:
					if !ok {
						return
					}
					if err := job.assert(); err != nil {
						panic(fmt.Errorf("error: %w", err))
					}
					numCheckedEntries.Add(1)
					wg.Done()
				}
			}
		}()
	}

	lastSecondTick := time.Now()
	lastNumHashes := uint64(0)
	lastNumEntries := uint64(0)
	lastNumBlocks := uint64(0)

	isFirstBlock := true
	numExpectedTotalBlocks := float64(432000)
	{
		actualStart, actualStop := limits.GetActualStartStopSlots()
		numExpectedTotalBlocks = float64(actualStop - actualStart + 1)
	}
	statTick := time.Second * 2

	go func() {
		currentSlotNumHashesAccumulator := uint64(0)

		// process the results from the workers:
		for result := range outputChan {
			switch resValue := result.Value.(type) {
			case error:
				panic(fmt.Errorf("error: %w", resValue))
			case uint64:
				if CalcEpochForSlot(resValue) == epochNum {
					numHashes.Add(currentSlotNumHashesAccumulator)
				} else {
					panic(fmt.Sprintf("error: unexpected slot %d from epoch %d", resValue, CalcEpochForSlot(resValue)))
				}
				currentSlotNumHashesAccumulator = 0
				numBlocks.Add(1)
				blockhash = solana.Hash(prevBlockHash)
				percentDone := float64(numBlocks.Load()) / numExpectedTotalBlocks * 100
				logMsg := fmt.Sprintf("Slot %d (%.2f%%)", resValue, percentDone)
				if time.Since(lastSecondTick) > statTick {
					lastSecondTick = time.Now()
					thisNumHashes := numHashes.Load()
					hashRate := int64(thisNumHashes - lastNumHashes)
					logMsg += fmt.Sprintf(" | %s hashes/s", humanize.Comma(hashRate))
					{
						thisNumEntries := numCheckedEntries.Load()
						entryRate := int64(thisNumEntries - lastNumEntries)
						logMsg += fmt.Sprintf(" | %s entries/s", humanize.Comma(entryRate))
						lastNumEntries = thisNumEntries
					}
					{ // estimated total time
						thisNumBlocks := numBlocks.Load()
						blockRate := int64(thisNumBlocks-lastNumBlocks) / int64(statTick.Seconds())
						logMsg += fmt.Sprintf(" | %s blocks/s", humanize.Comma(blockRate))
						estimatedTotalTime := time.Duration(numExpectedTotalBlocks/float64(blockRate)) * time.Second
						estimatedLeftTime := estimatedTotalTime - time.Since(startedAt)
						logMsg += fmt.Sprintf(" | %s left", estimatedLeftTime.Round(time.Second))
						lastNumBlocks = thisNumBlocks
					}

					lastNumHashes = thisNumHashes
				}
				{
					if isFirstBlock {
						isFirstBlock = false

						if err := limits.AssertFirstBlockSlot(resValue); err != nil {
							klog.Exitf(
								"PoH error: expected first slot in CAR to be %d, got %d (%s)",
								limits.FirstBlockSlot,
								resValue,
								blockhash,
							)
						} else {
							klog.Infof(
								"Assertion successful: First block in CAR for epoch %d is %d (blockhash=%s)",
								epochNum,
								resValue,
								blockhash,
							)
						}
						if err := limits.AssertFirstBlockhash(blockhash); err != nil {
							klog.Exitf(
								"PoH error: expected first blockhash in CAR to be %s (%d), got %s (%d)",
								limits.FirstBlockhash,
								limits.FirstBlockSlot,
								blockhash,
								resValue,
							)
						} else {
							klog.Infof(
								"Assertion successful: First blockhash in CAR for epoch %d is %s (block=%d)",
								epochNum,
								blockhash,
								resValue,
							)
						}
					}
				}
				if !noProgress {
					fmt.Printf("\r%s", greenBg(logMsg))
				}
			case *ipldbindcode.Transaction:
				txNode := resValue
				{
					sigs, err := readAllSignatures(txNode.Data.Bytes())
					if err != nil {
						panic(fmt.Sprintf("error: failed to read signature: %s", err))
					}
					for i := range sigs {
						signatureAccumulator = append(signatureAccumulator, sigs[i][:])
					}
				}
				waitResultsReceived.Done()
				numReceivedParsed.Add(-1)
			case *EntryAndSlot:
				onDone := func() {
					waitResultsReceived.Done()
					numReceivedParsed.Add(-1)
				}

				entry := resValue.Entry
				currentSlotNumHashesAccumulator += uint64(entry.NumHashes)

				if entry.NumHashes == 0 && len(entry.Transactions) == 0 {
					klog.Exitf("error: entry has no hashes and no transactions: %s", spew.Sdump(entry))
					onDone()
					continue
				}
				if prevBlockHash.IsZero() {
					panic("error: prevBlockHash is zero")
				}

				{

					jo := &entryCheckJob{
						Slot:       resValue.PreviousSlot,
						EntryIndex: resValue.EntryIndex,
						Prev:       *prevBlockHash.Clone(),
						Wanted:     poh.State(entry.Hash[:]),
						NumHashes:  uint64(entry.NumHashes),
						numtx:      len(entry.Transactions),
						accu:       cloneAccumulator(signatureAccumulator),
					}

					{
						copy(prevBlockHash[:], entry.Hash[:])
					}

					wg.Add(1)
					jobChan <- jo
					signatureAccumulator = make([][]byte, 0)
				}
				onDone()
			default:
				panic(fmt.Errorf("error: unexpected result type: %T", result.Value))
			}
		}
	}()
	lastBlockNum := -1
	entryIndex := -1

	startProcessingElements := !limits.StartSlot.IsSet()

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		object, err := rd.Next()
		if errors.Is(err, io.EOF) {
			fmt.Println("EOF")
			break
		}
		numNodesSeen++

		objectCID := object.Cid()
		// klog.Infof("Node %d: Object CID: %s\n", numNodesSeen, objectCID.String())
		rawData := object.RawData()
		if len(rawData) < 2 {
			return fmt.Errorf(
				"error: RawData too short (len=%d) for object %s at node %d",
				len(rawData),
				objectCID.String(),
				numNodesSeen,
			)
		}
		kind := iplddecoders.Kind(rawData[1])
		{
			if kind == iplddecoders.KindBlock {
				block, err := iplddecoders.DecodeBlock(rawData)
				if err != nil {
					return fmt.Errorf("failed to decode block: %w", err)
				}
				slot := uint64(block.Slot)
				{
					if limits.StartSlot.IsSet() {
						if limits.PreviousBlockSlot == slot {
							klog.Infof("Starting to process elements at slot %d (after custom start slot %d)", slot, limits.StartSlot.Get())
							startProcessingElements = true
							continue
						}
						if slot < (limits.StartSlot.Get()) {
							// skip this block
							// klog.Infof("Skipping block at slot %d (before custom start slot %d)", slot, limits.StartSlot.Get())
							continue
						}
					}
				}
				{
					if lastBlockNum == -1 {
						err := limits.AssertPreviousBlockSlot(uint64(block.Meta.Parent_slot))
						if err != nil {
							return fmt.Errorf("PoH error: expected previous epoch's last slot to be %d, got %d", limits.PreviousBlockSlot, block.Meta.Parent_slot)
						} else {
							klog.Infof(
								"Assertion successful: Previous epoch's last slot is %d (as set in the firstBlock.ParentSlot in the CAR file)",
								block.Meta.Parent_slot,
							)
						}
						// NOTE: previous blockhash is checked by building the chain of hashes on top of it.
					}
				}
				if block.Slot < lastBlockNum {
					return fmt.Errorf("unexpected block number: %d is less than %d", block.Slot, lastBlockNum)
				}
				lastBlockNum = block.Slot

				{
					workerInputChan <- slotSignal(
						uint64(lastBlockNum),
					)
					numBlocksWhereCheckedPoH++
					slot := uint64(lastBlockNum)

					if limits.EndSlot.IsSet() && slot == (limits.EndSlot.Get()) {
						// skip this block
						klog.Infof("Finishing PoH at block at slot %d", slot)
						break
					}
				}
			}
		}

		if kind == iplddecoders.KindBlock {
			entryIndex = -1
		}
		if kind == iplddecoders.KindEntry {
			entryIndex++
		}

		if kind == iplddecoders.KindEntry || kind == iplddecoders.KindTransaction {
			if !startProcessingElements {
				continue
			}
			waitExecuted.Add(1)
			waitResultsReceived.Add(1)
			numReceivedParsed.Add(1)
			workerInputChan <- newParserTask(
				uint64(lastBlockNum),
				entryIndex,
				object,
				func() {
					waitExecuted.Done()
				},
			)
		}
	}
	{
		klog.Infof("Waiting for all nodes to be parsed...")
		waitExecuted.Wait()
		klog.Infof("All nodes parsed.")

		klog.Infof("Waiting to receive all results...")
		close(workerInputChan)
		waitResultsReceived.Wait()
		klog.Infof("All results received")

		klog.Infof("Waiting for remaining jobs to finish...")
		wg.Wait()
		klog.Infof("All jobs finished")

		// print last blockHash:
		klog.Infof(
			"Last block hash for slot %d for epoch %d: %s",
			epochNum,
			lastBlockNum,
			blockhash,
		)
		klog.Infof(
			"Number of checked entries for epoch %d: %s",
			epochNum,
			humanize.Comma(int64(numCheckedEntries.Load())),
		)
		klog.Infof(
			"Number of hashes for epoch %d: %s",
			epochNum,
			humanize.Comma(int64(numHashes.Load())),
		)
		// if the last slot is for a different epoch, return an error:
		if CalcEpochForSlot(uint64(lastBlockNum)) != epochNum {
			return fmt.Errorf(
				"PoH error: last slot %d is not in epoch %d",
				lastBlockNum,
				epochNum,
			)
		}
		{
			if err := limits.AssertLastBlockSlot(uint64(lastBlockNum)); err != nil {
				return fmt.Errorf(
					"PoH error: expected last slot in CAR to be %d, got %d (%s)",
					limits.LastBlockSlot,
					lastBlockNum,
					blockhash,
				)
			} else {
				klog.Infof(
					"Assertion successful: Last block in CAR for epoch %d is %d (blockhash=%s)",
					epochNum,
					lastBlockNum,
					blockhash,
				)
			}
			if err := limits.AssertLastBlockhash(blockhash); err != nil {
				return fmt.Errorf(
					"PoH error: expected last blockhash in CAR to be %s (%d), got %s (%d)",
					limits.LastBlockhash,
					limits.LastBlockSlot,
					blockhash,
					lastBlockNum,
				)
			} else {
				klog.Infof(
					"Assertion successful: Last blockhash in CAR for epoch %d is %s (block=%d)",
					epochNum,
					blockhash,
					lastBlockNum,
				)
			}
		}

		mustNumHashesPerEpoch := uint64(345_600_000_000)
		if numHashes.Load() != mustNumHashesPerEpoch {
			klog.Warningf(
				"PoH warning: wrong number of hashes for epoch %d: expected %d, got %d",
				epochNum,
				mustNumHashesPerEpoch,
				numHashes.Load(),
			)
		}
	}
	return nil
}

func blackFg(s string) string {
	return "\033[30m" + s + "\033[0m"
}

func greenBg(s string) string {
	return blackFg("\033[42m" + s + "\033[0m")
}

func retryExponentialBackoff[T any](numRetries uint, f func() (T, error)) (T, error) {
	var errs []error
	var res T
	for i := uint(0); i < numRetries; i++ {
		var err error
		res, err = f()
		if err == nil {
			return res, nil
		}
		errs = append(errs, err)
		time.Sleep(time.Duration(1<<i) * time.Second)
	}
	return res, errors.Join(errs...)
}

func readAllSignatures(buf []byte) ([]solana.Signature, error) {
	decoder := bin.NewCompactU16Decoder(buf)
	numSigs, err := decoder.ReadCompactU16()
	if err != nil {
		return nil, err
	}
	if numSigs == 0 {
		return nil, fmt.Errorf("no signatures")
	}
	// check that there is at least 64 bytes * numSigs left:
	if decoder.Remaining() < (64 * numSigs) {
		return nil, fmt.Errorf("not enough bytes left to read %d signatures", numSigs)
	}

	sigs := make([]solana.Signature, numSigs)
	for i := 0; i < numSigs; i++ {
		numRead, err := decoder.Read(sigs[i][:])
		if err != nil {
			return nil, err
		}
		if numRead != 64 {
			return nil, fmt.Errorf("unexpected signature length %d", numRead)
		}
	}
	return sigs, nil
}

type slotSignal uint64

// Run implements concurrently.WorkFunction.
func (s slotSignal) Run(ctx context.Context) interface{} {
	return uint64(s)
}

type parserTask struct {
	slot       uint64
	entryIndex int
	blk        blocks.Block
	done       func()
}

func newParserTask(
	slot uint64,
	entryIndex int,
	blk blocks.Block,
	done func(),
) *parserTask {
	return &parserTask{
		slot:       slot,
		entryIndex: entryIndex,
		blk:        blk,
		done:       done,
	}
}

func (w parserTask) Run(ctx context.Context) interface{} {
	defer func() {
		w.done()
	}()

	block := w.blk

	switch iplddecoders.Kind(block.RawData()[1]) {
	case iplddecoders.KindTransaction:
		txNode, err := iplddecoders.DecodeTransaction(block.RawData())
		if err != nil {
			return fmt.Errorf("failed to decode transaction: %w", err)
		}
		return txNode
	case iplddecoders.KindEntry:
		entry, err := iplddecoders.DecodeEntry(block.RawData())
		if err != nil {
			return fmt.Errorf("failed to decode entry: %w", err)
		}
		return &EntryAndSlot{
			Entry:        entry,
			PreviousSlot: w.slot,
			EntryIndex:   w.entryIndex,
		}
	default:
		panic(fmt.Sprintf("error: unexpected kind: %s", iplddecoders.Kind(block.RawData()[1])))
	}
}

type EntryAndSlot struct {
	Entry        *ipldbindcode.Entry
	PreviousSlot uint64
	EntryIndex   int
}

func alignToPageSize(size int) int {
	alignment := int(os.Getpagesize())
	mask := alignment - 1
	mem := uintptr(size + alignment)
	return int((mem + uintptr(mask)) & ^uintptr(mask))
}

// CalcEpochForSlot returns the epoch for the given slot.
func CalcEpochForSlot(slot uint64) uint64 {
	return slot / EpochLen
}

const EpochLen = 432000
