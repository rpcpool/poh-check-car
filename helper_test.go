package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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

func TestGetEpochLimitsSkipsCustomBoundaries(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32009,"message":"slot not available"}}`))
	}))
	t.Cleanup(srv.Close)
	helper := NewHelper(1051, rpc.New(srv.URL))

	// A custom --start and --end replace both boundaries, so the in-progress next epoch is never queried.
	if _, err := helper.GetEpochLimits(false, false); err != nil {
		t.Fatalf("custom range: got %v", err)
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("custom range: made %d RPC calls, want 0", n)
	}
}
