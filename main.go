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

func main() {
	var (
		carPath          string
		prevHash         string
		numWorkers       uint
		noProgress       bool
		epochNum         int64
		assertLastBlock  uint64
		assertLastHash   string
		auto             bool
		rpcEndpoint      string
		assertFirstBlock int64
		assertFirstHash  string
	)
	flag.StringVar(&carPath, "car", "", "Path to CAR file")
	flag.StringVar(&prevHash, "prevhash", "", "Previous hash")
	flag.UintVar(&numWorkers, "workers", uint(runtime.NumCPU()), "Number of workers")
	flag.BoolVar(&noProgress, "no-progress", false, "Disable progress bar")
	flag.Int64Var(&epochNum, "epoch", -1, "Epoch number")
	flag.Uint64Var(&assertLastBlock, "assert-last-block", 0, "Assert last block")
	flag.StringVar(&assertLastHash, "assert-last-hash", "", "Assert last hash")
	flag.BoolVar(&auto, "auto", false, "Automatically find the slot number and hash for first and last slot of the epoch")
	flag.StringVar(&rpcEndpoint, "rpc", rpc.MainNetBeta.RPC, "RPC endpoint")
	flag.Int64Var(&assertFirstBlock, "assert-first-block", -1, "Assert first block")
	flag.StringVar(&assertFirstHash, "assert-first-hash", "", "Assert first blockhash")
	flag.Parse()

	if carPath == "" {
		klog.Exit("error: No CAR file given")
	}
	if !auto && prevHash == "" {
		klog.Exit("error: No previous hash given")
	}

	// check that the epoch number is set:
	if epochNum < 0 {
		klog.Exit("error: No epoch number given; please use the --epoch flag")
	}
	{
		if auto {
			if rpcEndpoint == "" {
				klog.Exit("error: No RPC endpoint given")
			}
			if rpcEndpoint == rpc.MainNetBeta.RPC {
				klog.Infof("Using default RPC endpoint (slow and rate-limited): %s", rpcEndpoint)
			} else {
				klog.Infof("Using RPC endpoint: %s", rpcEndpoint)
			}
			rpcClient := rpc.New(rpcEndpoint)
			{
				firtstSlot := uint64(epochNum * EpochLen)
				slotRangeEnd := firtstSlot + 1000
				// get the first slot of the epoch:
				blocks, err := rpcClient.GetBlocks(
					context.Background(),
					firtstSlot,
					&slotRangeEnd,
					rpc.CommitmentConfirmed,
				)
				if err != nil {
					klog.Exitf("error: failed to get first available block for epoch %d: %s", epochNum, err)
				}
				firstBlock := blocks[0]

				var parentSlot uint64
				var parentBlockhash solana.Hash
				var firstBlockhash solana.Hash
				no := false

				if epochNum == 0 {
					genesysHash, err := rpcClient.GetGenesisHash(
						context.Background(),
					)
					if err != nil {
						klog.Exitf("error: failed to get genesis hash: %s", err)
					}
					parentSlot = 0
					parentBlockhash = genesysHash

					block, err := rpcClient.GetBlockWithOpts(
						context.Background(),
						firstBlock,
						&rpc.GetBlockOpts{
							Encoding:           solana.EncodingBase64,
							Commitment:         rpc.CommitmentConfirmed,
							TransactionDetails: rpc.TransactionDetailsNone,
							Rewards:            &no,
						},
					)
					if err != nil {
						klog.Exitf("error: failed to get first block %d for epoch %d: %s", firstBlock, epochNum, err)
					}
					firstBlockhash = block.Blockhash
				} else {
					block, err := rpcClient.GetBlockWithOpts(
						context.Background(),
						firstBlock,
						&rpc.GetBlockOpts{
							Encoding:           solana.EncodingBase64,
							Commitment:         rpc.CommitmentConfirmed,
							TransactionDetails: rpc.TransactionDetailsNone,
							Rewards:            &no,
						},
					)
					if err != nil {
						klog.Exitf("error: failed to get first block %d for epoch %d: %s", firstBlock, epochNum, err)
					}
					parentSlot = block.ParentSlot
					parentBlockhash = block.PreviousBlockhash
					firstBlockhash = block.Blockhash
				}

				assertFirstBlock = int64(firstBlock)
				assertFirstHash = firstBlockhash.String()

				if epochNum == 0 {
					klog.Infof(
						"Epoch %d's first block is %d (blockhash=%s); the genesis hash is %s",
						epochNum,
						firstBlock,
						firstBlockhash,
						parentBlockhash,
					)
				} else {
					klog.Infof(
						"Epoch %d's first block is %d (blockhash=%s); its parent slot is %d which has blockhash %s",
						epochNum,
						firstBlock,
						firstBlockhash,
						parentSlot,
						parentBlockhash,
					)
				}
				prevHash = parentBlockhash.String()

				nextEpochNum := epochNum + 1
				{
					firstSlot := uint64(nextEpochNum * EpochLen)
					slotRangeEnd := firstSlot + 1000
					blocks, err := rpcClient.GetBlocks(
						context.Background(),
						firstSlot,
						&slotRangeEnd,
						rpc.CommitmentConfirmed,
					)
					if err != nil {
						klog.Exitf("error: failed to get last available block for epoch %d: %s", epochNum, err)
					}
					nextEpochFirstBlock := blocks[0]

					block, err := rpcClient.GetBlockWithOpts(
						context.Background(),
						nextEpochFirstBlock,
						&rpc.GetBlockOpts{
							Encoding:           solana.EncodingBase64,
							Commitment:         rpc.CommitmentConfirmed,
							TransactionDetails: rpc.TransactionDetailsNone,
							Rewards:            &no,
						},
					)
					if err != nil {
						klog.Exitf("error: failed to get first block %d for epoch %d: %s", nextEpochFirstBlock, nextEpochNum, err)
					}

					epochLastSlot := block.ParentSlot
					epochLastSlotBlockhash := block.PreviousBlockhash

					klog.Infof(
						"Epoch %d's last block is %d, which hash blockhash %s",
						epochNum,
						epochLastSlot,
						epochLastSlotBlockhash,
					)
					assertLastBlock = epochLastSlot
					assertLastHash = epochLastSlotBlockhash.String()
				}
			}
		}
	}

	if assertFirstBlock >= 0 {
		klog.Infof("- Will assert first block in the CAR to be %d", assertFirstBlock)
	}
	if assertFirstHash != "" {
		klog.Infof("- Will assert first blockhash in the CAR to be %s", assertFirstHash)
	}
	if assertLastBlock != 0 {
		klog.Infof("- Will assert last block in the CAR to be %d", assertLastBlock)
	}
	if assertLastHash != "" {
		klog.Infof("- Will assert last blockhash in the CAR to be %s", assertLastHash)
	}

	startedAt := time.Now()
	defer func() {
		klog.Infof("Took %s", time.Since(startedAt))
	}()
	ctx := context.Background()
	if err := checkCar(
		ctx,
		carPath,
		prevHash,
		numWorkers,
		noProgress,
		uint64(epochNum),
		// first:
		func() *uint64 {
			if assertFirstBlock >= 0 {
				v := uint64(assertFirstBlock)
				return &v
			}
			return nil
		}(),
		assertFirstHash,
		// last:
		assertLastBlock,
		assertLastHash,
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
	prevHash string,
	numWorkers uint,
	noProgress bool,
	epochNum uint64,
	// first:
	assertFirstSlot *uint64,
	assertFirstHash string,
	// last:
	assertLastSlot uint64,
	assertLastHash string,
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
	defer func() {
		klog.Infof("Finished in %s", time.Since(startedAt))
		klog.Infof("Read %d nodes from CAR file", numNodesSeen)
	}()

	prevBlockHashRaw, err := solana.PublicKeyFromBase58(prevHash)
	if err != nil {
		return fmt.Errorf("failed to parse previous hash %q: %w", prevHash, err)
	}
	prevBlockHash := poh.State(prevBlockHashRaw)
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
	blockhash := [32]byte{}
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

	isFirstBlock := true

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
				percentDone := float64(numBlocks.Load()) / float64(432000) * 100
				logMsg := fmt.Sprintf("Slot %d (%.2f%%)", resValue, percentDone)
				if time.Since(lastSecondTick) > time.Second {
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
					{ // estimated total time is 345,600,000,000/hashRate seconds
						estimatedTotalTime := time.Duration(345_600_000_000/hashRate) * time.Second
						estimatedLeftTime := estimatedTotalTime - time.Since(startedAt)
						logMsg += fmt.Sprintf(" | %s left", estimatedLeftTime.Round(time.Second))
					}

					lastNumHashes = thisNumHashes
				}
				{
					if isFirstBlock {
						isFirstBlock = false
						if assertFirstSlot != nil {
							if *assertFirstSlot != resValue {
								klog.Exitf(
									"PoH error: expected first slot to be %d, got %d (%s)",
									*assertFirstSlot,
									resValue,
									solana.Hash(blockhash),
								)
							} else {
								klog.Infof(
									"Assertion successful: First block for epoch %d is %d (blockhash=%s)",
									epochNum,
									resValue,
									solana.Hash(blockhash),
								)
							}
						}
						if assertFirstHash != "" {
							if assertFirstHash != solana.Hash(blockhash).String() {
								klog.Exitf(
									"PoH error: expected first hash to be %s (%d), got %s (%d)",
									assertFirstHash,
									*assertFirstSlot,
									solana.Hash(blockhash),
									resValue,
								)
							} else {
								klog.Infof(
									"Assertion successful: First blockhash for epoch %d is %s (block=%d)",
									epochNum,
									solana.Hash(blockhash),
									resValue,
								)
							}
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

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		block, err := rd.Next()
		if errors.Is(err, io.EOF) {
			fmt.Println("EOF")
			break
		}
		numNodesSeen++
		kind := iplddecoders.Kind(block.RawData()[1])
		{
			if kind == iplddecoders.KindBlock {
				block, err := iplddecoders.DecodeBlock(block.RawData())
				if err != nil {
					return fmt.Errorf("failed to decode block: %w", err)
				}
				if block.Slot < lastBlockNum {
					return fmt.Errorf("unexpected block number: %d is less than %d", block.Slot, lastBlockNum)
				}
				lastBlockNum = block.Slot
			}
		}

		if kind == iplddecoders.KindBlock {
			entryIndex = -1
		}
		if kind == iplddecoders.KindEntry {
			entryIndex++
		}

		if kind == iplddecoders.KindBlock {
			workerInputChan <- slotSignal(
				uint64(lastBlockNum),
			)
		}
		if kind == iplddecoders.KindEntry || kind == iplddecoders.KindTransaction {
			waitExecuted.Add(1)
			waitResultsReceived.Add(1)
			numReceivedParsed.Add(1)
			workerInputChan <- newParserTask(
				uint64(lastBlockNum),
				entryIndex,
				block,
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

		klog.Infof("Waiting for all assertions to finish...")
		wg.Wait()
		klog.Infof("All assertions finished")

		// print last blockHash:
		klog.Infof(
			"Last block hash for slot %d for epoch %d: %s",
			epochNum,
			lastBlockNum,
			solana.Hash(blockhash),
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
			if assertLastSlot != 0 {
				if assertLastSlot != uint64(lastBlockNum) {
					return fmt.Errorf(
						"PoH error: expected last slot to be %d, got %d (%s)",
						assertLastSlot,
						lastBlockNum,
						solana.Hash(blockhash),
					)
				} else {
					klog.Infof(
						"Assertion successful: Last block for epoch %d is %d (blockhash=%s)",
						epochNum,
						lastBlockNum,
						solana.Hash(blockhash),
					)
				}
			}
			if assertLastHash != "" {
				if assertLastHash != solana.Hash(blockhash).String() {
					return fmt.Errorf(
						"PoH error: expected last hash to be %s (%d), got %s (%d)",
						assertLastHash,
						assertLastSlot,
						solana.Hash(blockhash),
						lastBlockNum,
					)
				} else {
					klog.Infof(
						"Assertion successful: Last blockhash for epoch %d is %s (block=%d)",
						epochNum,
						solana.Hash(blockhash),
						lastBlockNum,
					)
				}
			}
		}

		mustNumHashesPerEpoch := uint64(345_600_000_000)
		if numHashes.Load() != mustNumHashesPerEpoch {
			return fmt.Errorf(
				"PoH error: wrong number of hashes for epoch %d: expected %d, got %d",
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
