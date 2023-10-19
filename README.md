# poh-check-car

Validate PoH (proof of history) on the data in an epoch CAR file.

## Usage

```bash
$ poh-check-car --workers=12 \
	--car=/media/laptop/solana-history/cars/epoch-0.car \
	--prevhash=5eykt4UsFv8P8NJdTREpY1vzqKqZKvdpKuc147dw2N9d
```

To find the prevhash, use 

```bash
POH_CHECK_EPOCH=488
POH_CHECK_EPOCH_FIRST_SLOT=$((POH_CHECK_EPOCH * 432000))

# use getBlocks to find the actual slot
POH_CHECK_EPOCH_FIRST_SLOT=$(curl https://api.mainnet-beta.solana.com -X POST -H "Content-Type: application/json" -d '
  {"jsonrpc": "2.0", "id": "99", "method": "getBlocks", "params": ['$POH_CHECK_EPOCH_FIRST_SLOT', '$((POH_CHECK_EPOCH_FIRST_SLOT + 1000))'] }' | jq -r '.result[0]'
)
POH_CHECK_PREVIOUS_BLOCKHASH=$(curl https://api.mainnet-beta.solana.com \
	-X POST \
	-H "Content-Type: application/json" \
	--data '{"jsonrpc":"2.0","id":1, "method":"getBlock","params":['$POH_CHECK_EPOCH_FIRST_SLOT',{"transactionDetails":"none","rewards":false}]}' | jq -r '.result.previousBlockhash')

echo "The previous blockhash for epoch $POH_CHECK_EPOCH 's first slot $POH_CHECK_EPOCH_FIRST_SLOT is $POH_CHECK_PREVIOUS_BLOCKHASH"

echo "Use that as the --prevhash argument to poh-check-car like this:"

echo "poh-check-car --workers=12 --car=/media/laptop/solana-history/cars/epoch-$POH_CHECK_EPOCH.car --prevhash=$POH_CHECK_PREVIOUS_BLOCKHASH"
```

NOTE: for epoch 0, the `--prevhash` is the genesis hash.

```bash
$ curl https://api.mainnet-beta.solana.com \
	-X POST \
	-H "Content-Type: application/json" \
	--data '{"jsonrpc":"2.0","id":1, "method":"getGenesisHash"}'

{"jsonrpc":"2.0","result":"5eykt4UsFv8P8NJdTREpY1vzqKqZKvdpKuc147dw2N9d","id":1}
```
