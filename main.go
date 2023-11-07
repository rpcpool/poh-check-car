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
		carPath    string
		prevHash   string
		numWorkers uint
		noProgress bool
		epochNum   int64
	)
	flag.StringVar(&carPath, "car", "", "Path to CAR file")
	flag.StringVar(&prevHash, "prevhash", "", "Previous hash")
	flag.UintVar(&numWorkers, "workers", uint(runtime.NumCPU()), "Number of workers")
	flag.BoolVar(&noProgress, "no-progress", false, "Disable progress bar")
	flag.Int64Var(&epochNum, "epoch", -1, "Epoch number")
	flag.Parse()

	if carPath == "" {
		klog.Exit("error: No CAR file given")
	}
	if prevHash == "" {
		klog.Exit("error: No previous hash given")
	}

	// check that the epoch number is set:
	if epochNum < 0 {
		klog.Exit("error: No epoch number given; please use the --epoch flag")
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
	fmt.Println("PrevBlockHash:", solana.Hash(prevBlockHash))

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

	var sigs []solana.Signature
	for i := 0; i < numSigs; i++ {
		var sig solana.Signature
		numRead, err := decoder.Read(sig[:])
		if err != nil {
			return nil, err
		}
		if numRead != 64 {
			return nil, fmt.Errorf("unexpected signature length %d", numRead)
		}
		sigs = append(sigs, sig)
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
