// Copyright 2024 ChainSafe Systems (ON)
// SPDX-License-Identifier: LGPL-3.0-only

package triedb

import (
	"bytes"

	"github.com/ChainSafe/gossamer/pkg/trie"
	nibbles "github.com/ChainSafe/gossamer/pkg/trie/codec"
	"github.com/ChainSafe/gossamer/pkg/trie/triedb/codec"
)

type iteratorState struct {
	parentFullKey []byte            // key of the parent node of the actual node
	node          codec.EncodedNode // actual node
}

// fullKeyNibbles return the full key of the node contained in this state
// child is the child where the node is stored in the parent node
func (s *iteratorState) fullKeyNibbles(child *int) []byte {
	fullKey := bytes.Join([][]byte{s.parentFullKey, s.node.GetPartialKey()}, nil)
	if child != nil {
		return bytes.Join([][]byte{fullKey, {byte(*child)}}, nil)
	}

	return nibbles.NibblesToKeyLE(fullKey)
}

type TrieDBIterator struct {
	db        *TrieDB          // trie to iterate over
	nodeStack []*iteratorState // Pending nodes to visit
}

func NewTrieDBIterator(trie *TrieDB) *TrieDBIterator {
	rootNode, err := trie.getRootNode()
	if err != nil {
		panic("trying to create trie iterator with incomplete trie DB")
	}
	return &TrieDBIterator{
		db: trie,
		nodeStack: []*iteratorState{
			{
				node: rootNode,
			},
		},
	}
}

func NewPrefixedTrieDBIterator(trie *TrieDB, prefix []byte) *TrieDBIterator {
	nodeAtPrefix, err := trie.getNodeAt(prefix)
	if err != nil {
		panic("trying to create trie iterator with incomplete trie DB")
	}

	return &TrieDBIterator{
		db: trie,
		nodeStack: []*iteratorState{
			{
				parentFullKey: prefix[:len(nodeAtPrefix.GetPartialKey())-1],
				node:          nodeAtPrefix,
			},
		},
	}
}

// nextToVisit sets the next node to visit in the iterator
func (i *TrieDBIterator) nextToVisit(parentKey []byte, node codec.EncodedNode) {
	i.nodeStack = append(i.nodeStack, &iteratorState{
		parentFullKey: parentKey,
		node:          node,
	})
}

// nextState pops the next node to visit from the stack
// warn: this function does not check if the node stack is empty
// this check should be made by the caller
func (i *TrieDBIterator) nextState() *iteratorState {
	currentState := i.nodeStack[len(i.nodeStack)-1]
	i.nodeStack = i.nodeStack[:len(i.nodeStack)-1]
	return currentState
}

func (i *TrieDBIterator) NextEntry() *trie.Entry {
	for len(i.nodeStack) > 0 {
		currentState := i.nextState()
		currentNode := currentState.node

		switch n := currentNode.(type) {
		case codec.Leaf:
			key := currentState.fullKeyNibbles(nil)
			value := i.db.Get(key)
			return &trie.Entry{Key: key, Value: value}
		case codec.Branch:
			// Reverse iterate over children because we are using a LIFO stack
			// and we want to visit the leftmost child first
			for idx := len(n.Children) - 1; idx >= 0; idx-- {
				child := n.Children[idx]
				if child != nil {
					childNode, err := i.db.getNode(child)
					if err != nil {
						panic(err)
					}
					i.nextToVisit(currentState.fullKeyNibbles(&idx), childNode)
				}
			}
			if n.GetValue() != nil {
				key := currentState.fullKeyNibbles(nil)
				value := i.db.Get(key)
				return &trie.Entry{Key: key, Value: value}
			}
		}
	}

	return nil
}

// NextKey performs a depth-first search on the trie and returns the next key
// based on the current state of the iterator.
func (i *TrieDBIterator) NextKey() []byte {
	entry := i.NextEntry()
	if entry != nil {
		return entry.Key
	}
	return nil
}

func (i *TrieDBIterator) NextKeyFunc(predicate func(nextKey []byte) bool) (nextKey []byte) {
	for entry := i.NextEntry(); entry != nil; entry = i.NextEntry() {
		if predicate(entry.Key) {
			return entry.Key
		}
	}
	return nil
}

func (i *TrieDBIterator) Seek(targetKey []byte) {
	for key := i.NextKey(); bytes.Compare(key, targetKey) < 0; key = i.NextKey() {
	}
}

var _ trie.TrieIterator = (*TrieDBIterator)(nil)
