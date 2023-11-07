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
POH_CHECK_EPOCH=131
POH_CHECK_EPOCH_FIRST_SLOT=$((POH_CHECK_EPOCH * 432000))

# use getBlocks to find the actual slot
POH_CHECK_EPOCH_FIRST_SLOT=$(curl https://api.mainnet-beta.solana.com -X POST -H "Content-Type: application/json" -d '
  {"jsonrpc": "2.0", "id": "99", "method": "getBlocks", "params": ['$POH_CHECK_EPOCH_FIRST_SLOT', '$((POH_CHECK_EPOCH_FIRST_SLOT + 1000))'] }' | jq -r '.result[0]'
)

# check that POH_CHECK_EPOCH_FIRST_SLOT has a value, and is a number
if [[ -z "$POH_CHECK_EPOCH_FIRST_SLOT" || ! "$POH_CHECK_EPOCH_FIRST_SLOT" =~ ^[0-9]+$ ]]; then
  echo "Failed to find the first slot for epoch $POH_CHECK_EPOCH"
  exit 1
fi

POH_CHECK_FIRST_SLOT_DATA=$(curl https://api.mainnet-beta.solana.com \
	-X POST \
	-H "Content-Type: application/json" \
	--data '{"jsonrpc":"2.0","id":1, "method":"getBlock","params":['$POH_CHECK_EPOCH_FIRST_SLOT',{"transactionDetails":"none","rewards":false}]}')

POH_CHECK_PREVIOUS_BLOCKHASH=$(echo $POH_CHECK_FIRST_SLOT_DATA | jq -r '.result.previousBlockhash')
POH_CHECK_PREVIOUS_SLOT=$(echo $POH_CHECK_FIRST_SLOT_DATA | jq -r '.result.parentSlot')

# check that POH_CHECK_PREVIOUS_BLOCKHASH has a value, and is a string without spaces and is base58
if [[ -z "$POH_CHECK_PREVIOUS_BLOCKHASH" || ! "$POH_CHECK_PREVIOUS_BLOCKHASH" =~ ^[1-9A-HJ-NP-Za-km-z]+$ ]]; then
  echo "Failed to find the previous blockhash for epoch $POH_CHECK_EPOCH 's first slot $POH_CHECK_EPOCH_FIRST_SLOT"
  exit 1
fi

echo "Epoch $POH_CHECK_EPOCH 's first slot is $POH_CHECK_EPOCH_FIRST_SLOT; its parent slot is $POH_CHECK_PREVIOUS_SLOT which has blockhash $POH_CHECK_PREVIOUS_BLOCKHASH"

echo "Use that as the --prevhash argument to poh-check-car like this:"

echo "poh-check-car --workers=12 --car=/media/laptop/solana-history/cars/epoch-$POH_CHECK_EPOCH.car --prevhash=$POH_CHECK_PREVIOUS_BLOCKHASH --epoch=$POH_CHECK_EPOCH"

# now calculate the last slot of the epoch, and the last blockhash by using the previous slot of next epoch
POH_NEXT_EPOCH=$(($POH_CHECK_EPOCH + 1))
POH_NEXT_EPOCH_FIRST_SLOT=$((POH_NEXT_EPOCH * 432000))

# use getBlocks to find the actual slot
POH_NEXT_EPOCH_FIRST_SLOT=$(curl https://api.mainnet-beta.solana.com -s -X POST -H "Content-Type: application/json" -d '
  {"jsonrpc": "2.0", "id": "99", "method": "getBlocks", "params": ['$POH_NEXT_EPOCH_FIRST_SLOT', '$((POH_NEXT_EPOCH_FIRST_SLOT + 1000))'] }' | jq -r '.result[0]'
)

# check that POH_NEXT_EPOCH_FIRST_SLOT has a value, and is a number
if [[ -z "$POH_NEXT_EPOCH_FIRST_SLOT" || ! "$POH_NEXT_EPOCH_FIRST_SLOT" =~ ^[0-9]+$ ]]; then
  echo "Failed to find the first slot for epoch $POH_NEXT_EPOCH"
  exit 1
fi

POH_NEXT_EPOCH_FIRST_SLOT_DATA=$(curl https://api.mainnet-beta.solana.com \
	-X POST -s \
	-H "Content-Type: application/json" \
	--data '{"jsonrpc":"2.0","id":1, "method":"getBlock","params":['$POH_NEXT_EPOCH_FIRST_SLOT',{"transactionDetails":"none","rewards":false}]}')

POH_NEXT_EPOCH_PREVIOUS_BLOCKHASH=$(echo $POH_NEXT_EPOCH_FIRST_SLOT_DATA | jq -r '.result.previousBlockhash')
POH_NEXT_EPOCH_PREVIOUS_SLOT=$(echo $POH_NEXT_EPOCH_FIRST_SLOT_DATA | jq -r '.result.parentSlot')

# check that POH_NEXT_EPOCH_PREVIOUS_BLOCKHASH has a value, and is a string without spaces and is base58
if [[ -z "$POH_NEXT_EPOCH_PREVIOUS_BLOCKHASH" || ! "$POH_NEXT_EPOCH_PREVIOUS_BLOCKHASH" =~ ^[1-9A-HJ-NP-Za-km-z]+$ ]]; then
  echo "Failed to find the previous blockhash for epoch $POH_NEXT_EPOCH 's first slot $POH_NEXT_EPOCH_FIRST_SLOT"
  exit 1
fi

echo "Epoch $POH_CHECK_EPOCH 's last slot is $POH_NEXT_EPOCH_PREVIOUS_SLOT with blockhash $POH_NEXT_EPOCH_PREVIOUS_BLOCKHASH"

echo "Use --assert-last-slot=$POH_NEXT_EPOCH_PREVIOUS_SLOT --assert-last-hash=$POH_NEXT_EPOCH_PREVIOUS_BLOCKHASH to check the last slot and blockhash of the epoch"

echo "If you don't want to see the progress bar, use --no-progress"
```

NOTE: for epoch 0, the `--prevhash` is the genesis hash.

```bash
$ curl https://api.mainnet-beta.solana.com \
	-X POST \
	-H "Content-Type: application/json" \
	--data '{"jsonrpc":"2.0","id":1, "method":"getGenesisHash"}'

{"jsonrpc":"2.0","result":"5eykt4UsFv8P8NJdTREpY1vzqKqZKvdpKuc147dw2N9d","id":1}
```

If you don't want to see the progress bar, use `--no-progress`.
