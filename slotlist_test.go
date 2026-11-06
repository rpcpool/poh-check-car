package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/gagliardetto/solana-go/rpc"
)

func TestSlotList(t *testing.T) {
	tests := []struct {
		name    string
		car     []uint64
		wantErr string
	}{
		{name: "match", car: []uint64{10, 11, 13}},
		{name: "extra slot", car: []uint64{10, 11, 12, 13}, wantErr: "slot 12 is in the CAR, but the RPC has no block there (next RPC block is 13)"},
		{name: "missing slot", car: []uint64{10, 13}, wantErr: "the RPC has a block at slot 11, but the CAR skips to 13"},
		{name: "past the end", car: []uint64{10, 11, 13, 14}, wantErr: "slot 14 is in the CAR, but the RPC lists no more blocks"},
		{name: "short CAR", car: []uint64{10, 11}, wantErr: "the CAR ends before slot 13: the RPC lists 1 more blocks"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l, err := newSlotList([]uint64{10, 11, 13})
			if err != nil {
				t.Fatal(err)
			}
			for _, slot := range tt.car {
				if err = l.see(slot); err != nil {
					break
				}
			}
			if err == nil {
				err = l.finish()
			}
			checkErr(t, "slot list", err, tt.wantErr)
		})
	}

	if _, err := newSlotList([]uint64{10, 10}); err == nil {
		t.Fatal("duplicate RPC slot: want an error")
	}
	var off *slotList
	if off.see(5) != nil || off.finish() != nil {
		t.Fatal("nil slot list must accept everything")
	}
}

func TestGetBlockListChunks(t *testing.T) {
	var ranges [][2]uint64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Method string `json:"method"`
			Params []any  `json:"params"`
		}
		if err := json.Unmarshal(body, &req); err != nil || req.Method != "getBlocks" {
			t.Errorf("unexpected request %s", body)
			return
		}
		if cfg, _ := req.Params[2].(map[string]any); cfg["commitment"] != "finalized" {
			t.Errorf("commitment = %v, want finalized", cfg["commitment"])
		}
		lo, hi := uint64(req.Params[0].(float64)), uint64(req.Params[1].(float64))
		ranges = append(ranges, [2]uint64{lo, hi})
		// One block at the start of each range, plus the last slot.
		res := []uint64{lo}
		if hi == 25_000 {
			res = append(res, hi)
		}
		out, _ := json.Marshal(res)
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":%s}`, out)
	}))
	t.Cleanup(srv.Close)

	got, err := NewHelper(0, rpc.New(srv.URL)).GetBlockList(0, 25_000)
	if err != nil {
		t.Fatal(err)
	}
	wantRanges := [][2]uint64{{0, 9_999}, {10_000, 19_999}, {20_000, 25_000}}
	if !slices.Equal(ranges, wantRanges) {
		t.Fatalf("ranges = %v, want %v", ranges, wantRanges)
	}
	if want := []uint64{0, 10_000, 20_000, 25_000}; !slices.Equal(got, want) {
		t.Fatalf("slots = %v, want %v", got, want)
	}
}
