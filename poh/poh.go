// From: https://github.com/firedancer-io/radiance/tree/main/pkg/poh
//
// Package poh implements the "Proof-of-History" delay function.
//
// Similarly to how Bitcoin Proof-of-Work proves computing power,
// Solana Proof-of-History exploits single-thread latency to prove that time has passed.
//
// At its core, PoH is a SHA256 hash chain that grows as fast as possible.
package poh

import (
	"encoding/hex"
	"sync"

	"github.com/minio/sha256-simd"
)

var bufPool = sync.Pool{
	New: func() interface{} {
		return new([64]byte)
	},
}

func reset(buf *[64]byte) {
	for i := range buf {
		buf[i] = 0
	}
}

func getBuf() *[64]byte {
	got := bufPool.Get().(*[64]byte)
	reset(got)
	return got
}

func putBuf(buf *[64]byte) {
	bufPool.Put(buf)
}

// State is the internal state of the PoH delay function.
type State [32]byte

// Record "mixes in" a 32-byte value using a new PoH iteration.
func (s *State) Record(mixin *[32]byte) {
	buf := getBuf()

	copy(buf[:32], s[:])
	copy(buf[32:], mixin[:])
	*s = sha256.Sum256(buf[:])

	putBuf(buf)
}

// Hash executes a number of PoH iterations.
func (s *State) Hash(n uint) {
	for i := uint(0); i < n; i++ {
		*s = sha256.Sum256(s[:])
	}
}

func (s State) String() string {
	return hex.EncodeToString(s[:])
}

func (s *State) Clone() *State {
	var cloned State
	copy(cloned[:], s[:])
	return &cloned
}

// IsZero returns true if the state is zero.
func (s State) IsZero() bool {
	return s == State{}
}
