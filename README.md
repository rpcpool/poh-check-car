# poh-check-car

Validate PoH (proof of history) on the data in an epoch CAR file.

## Usage


Automatically find all the limits and assertions via RPC:

```bash
$ poh-check-car \
	--workers=12 \
	--epoch=0 \
	--car=/media/runner/solana-2/cars/epoch-0.car
```

OR provide the limits and assertions manually:

```bash
$ poh-check-car \
	--workers=12 \
	--epoch=0 \
	--car=/media/laptop/solana-history/cars/epoch-0.car \
	--prev-hash=5eykt4UsFv8P8NJdTREpY1vzqKqZKvdpKuc147dw2N9d \
	--first-slot=0 \
	--first-hash=4sGjMW1sUnHzSxGspuhpqLDx6wiyjNtZAMdL4VZHirAn \
	--last-slot=431999 \
	--last-hash=4WxiJQL77oMLb8mkQ3vQynd6DVhhqttdZkC1gQKy1Dcy
```

NOTE: for epoch 0, the `--prev-hash` is the genesis hash.

```bash
$ curl https://api.mainnet-beta.solana.com \
	-X POST \
	-H "Content-Type: application/json" \
	--data '{"jsonrpc":"2.0","id":1, "method":"getGenesisHash"}'

{"jsonrpc":"2.0","result":"5eykt4UsFv8P8NJdTREpY1vzqKqZKvdpKuc147dw2N9d","id":1}
```

If you don't want to see the progress bar, use `--no-progress`.

## Clusters and Alpenglow

Epoch boundaries come from the RPC's `getEpochSchedule`. With `--network=testnet` (or `mainnet`, `devnet`), the named cluster's schedule is used instead, and the run fails unless the RPC's genesis hash is that cluster's.

TowerBFT blocks must have 64 ticks per slot since their parent, each with the same hash count (learned from the first block); breaks are logged as a warning.

Blocks after the Alpenglow genesis slot must carry block markers and follow Alpenglow PoH: every entry has `num_hashes == 1` and the block ends with a single tick. The genesis slot is read from the genesis certificate marker in the migration epoch's CAR; for other epochs, pass `--alpenglow-genesis-slot` to also catch Alpenglow blocks that lost their markers.
