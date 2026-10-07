package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gagliardetto/solana-go/rpc"
	"github.com/rpcpool/yellowstone-faithful/slottools"
)

// fakeRPC answers getGenesisHash and getEpochSchedule like a node of the given cluster.
func fakeRPC(t *testing.T, genesisHash string, schedule string) *Helper {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := new(strings.Builder)
		_, _ = io.Copy(buf, r.Body)
		result := schedule
		if strings.Contains(buf.String(), "getGenesisHash") {
			result = `"` + genesisHash + `"`
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":` + result + `}`))
	}))
	t.Cleanup(srv.Close)
	return NewHelper(1051, rpc.New(srv.URL))
}

const testnetSchedule = `{"slotsPerEpoch":432000,"leaderScheduleSlotOffset":432000,"warmup":true,"firstNormalEpoch":14,"firstNormalSlot":524256}`

func TestGetEpochSchedule(t *testing.T) {
	testnet := fakeRPC(t, "4uhcVJyU9pJkvQyS88uRDiswHXSCkY3zQawwpjk2NsNY", testnetSchedule)

	got, err := testnet.GetEpochSchedule("")
	if err != nil || got != slottools.TestnetEpochSchedule {
		t.Fatalf("schedule from RPC: got %v, %v", got, err)
	}
	got, err = testnet.GetEpochSchedule("testnet")
	if err != nil || got != slottools.TestnetEpochSchedule {
		t.Fatalf("matching --network: got %v, %v", got, err)
	}
	if _, err := testnet.GetEpochSchedule("mainnet"); err == nil || !strings.Contains(err.Error(), "--rpc is not a mainnet node") {
		t.Fatalf("testnet RPC with --network mainnet: got %v", err)
	}
	if _, err := testnet.GetEpochSchedule("mars"); err == nil {
		t.Fatal("unknown --network: want an error")
	}
}
