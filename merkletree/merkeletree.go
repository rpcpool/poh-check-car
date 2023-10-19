// From: https://github.com/firedancer-io/radiance/tree/main/pkg/merkletree
//
// Package merkletree is a binary SHA256 Merkle tree.
//
// The binary Merkle tree contains two types of nodes:
// leaves (type 0x00) and intermediate nodes (type 0x01).
//
// The leaf hash is a hash of its data, prefixed by a zero byte:
// `SHA256(0x00 || leaf)`.
//
// The intermediate hash is a hash of the child hashes, prefixed by a one byte:
// `SHA256(0x01 || left || right)`
//
// # Construction
//
// Solana consensus relies on deterministic construction of Merkle trees.
//
// The "canoical" construction method arranges the tree into "layers" (identified by node distance to root),
// with the lowest layer always consisting of leaf nodes.
//
// If the lowest layer contains more than one leaf, recursively construct upper layers
// of intermediate nodes that each hash a pair of two of the lower layer's nodes in order.
//
// When any upper layer hashes an uneven amount of nodes,
// the last intermediate node shall hash the same lower node twice.
package merkletree

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/davecgh/go-spew/spew"
)

// One byte type prefix used as a hash domain.
const (
	TypeLeaf         = 0x00
	TypeIntermediate = 0x01
)

// Nodes stores the hashes of all nodes, excluding node type and content.
type Nodes struct {
	NumLeaves uint
	Nodes     [][32]byte
}

// GetRoot returns the root hash.
// Returns zero if tree is empty.
func (n *Nodes) GetRoot() (out *[32]byte) {
	if len(n.Nodes) == 0 {
		return nil
	}
	return &n.Nodes[len(n.Nodes)-1]
}

// TODO provide a method for memory-efficient Merkle construction when only the root is requested.
//      Can be implemented using recursion root level downwards

// HashNodes constructs proof data from a set of leaves.
//
// Port of solana_merkle_tree::MerkleTree::new
func HashNodes(leaves [][]byte) (out Nodes) {
	// Construct lowest layer by hashing every leaf.
	out.NumLeaves = uint(len(leaves))
	for _, leaf := range leaves {
		out.Nodes = append(out.Nodes, HashLeaf(leaf))
	}

	// Iteratively hash into upper layers until we reach the root.
	levelLen := nextLevelLen(out.NumLeaves)
	levelOff := out.NumLeaves // offset in node list of cur level
	prevLevelLen := out.NumLeaves
	prevLevelOff := uint(0) // offset in node list of prev level
	for levelLen > 0 {
		for i := uint(0); i < levelLen; i++ {
			prevLevelIdx := i * 2
			prevLevelNodeIdx := prevLevelOff + prevLevelIdx

			// Read back two nodes from previous layer.
			var left, right *[32]byte
			left = &out.Nodes[prevLevelNodeIdx]
			if prevLevelIdx+1 < prevLevelLen {
				right = &out.Nodes[prevLevelNodeIdx+1]
			} else {
				// Only one node left in the lower layer,
				// therefore hash remaining node twice.
				right = left
			}

			// Construct intermediate node.
			node := HashIntermediate(left, right)
			out.Nodes = append(out.Nodes, node)
		}

		// Move on to next layer.
		prevLevelOff = levelOff
		prevLevelLen = levelLen
		levelOff += levelLen
		levelLen = nextLevelLen(levelLen)
	}

	return
}

// HashLeaf returns the hash of a leaf node.
func HashLeaf(data []byte) (out [32]byte) {
	h := sha256.New()
	h.Write([]byte{TypeLeaf})
	h.Write(data)
	h.Sum(out[:0])
	return
}

// HashIntermediate returns the hash of an intermediate node.
func HashIntermediate(left *[32]byte, right *[32]byte) (out [32]byte) {
	h := sha256.New()
	h.Write([]byte{TypeIntermediate})
	h.Write(left[:])
	h.Write(right[:])
	h.Sum(out[:0])
	return
}

// nextLevelLen returns the amount of nodes in the layer above the current one,
// given the number of nodes in the current layer.
func nextLevelLen(levelLen uint) uint {
	//    fn next_level_len(level_len: usize) -> usize {
	//         if level_len == 1 {
	//             0
	//         } else {
	//             (level_len + 1) / 2
	//         }
	//     }
	if levelLen == 1 {
		return 0
	}
	return (levelLen + 1) / 2
}

func init() {
	// const TEST: &[&[u8]] = &[
	//     b"my", b"very", b"eager", b"mother", b"just", b"served", b"us", b"nine", b"pizzas",
	//     b"make", b"prime",
	// ];
	// const BAD: &[&[u8]] = &[b"bad", b"missing", b"false"];
	//     fn test_tree_from_many() {
	//     let mt = MerkleTree::new(TEST);
	//     // This golden hash will need to be updated whenever the contents of `TEST` change in any
	//     // way, including addition, removal and reordering or any of the tree calculation algo
	//     // changes
	//     let bytes = hex::decode("b40c847546fdceea166f927fc46c5ca33c3638236a36275c1346d3dffb84e1bc")
	//         .unwrap();
	//     let expected = Hash::new(&bytes);
	//     assert_eq!(mt.get_root(), Some(&expected));
	// }
	TEST := [][]byte{
		[]byte("my"),
		[]byte("very"),
		[]byte("eager"),
		[]byte("mother"),
		[]byte("just"),
		[]byte("served"),
		[]byte("us"),
		[]byte("nine"),
		[]byte("pizzas"),
		[]byte("make"),
		[]byte("prime"),
	}

	mt := HashNodes(TEST)
	// This golden hash will need to be updated whenever the contents of `TEST` change in any
	// way, including addition, removal and reordering or any of the tree calculation algo
	// changes
	bytes, err := hex.DecodeString("b40c847546fdceea166f927fc46c5ca33c3638236a36275c1346d3dffb84e1bc")
	if err != nil {
		panic(err)
	}

	expected := bytesToHash(bytes)
	if false {
		spew.Dump(mt)
		spew.Dump(bytes)
		spew.Dump(expected)

		fmt.Println("mt.GetRoot()", spew.Sdump(mt.GetRoot()))
		fmt.Println("expected.GetRoot()", spew.Sdump(expected))
	}

	if *mt.GetRoot() != expected {
		panic("bad hash")
	}
}

func bytesToHash(b []byte) (out [32]byte) {
	if len(b) != 32 {
		panic("bad hash")
	}
	copy(out[:], b)
	return
}
