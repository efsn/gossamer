// Copyright 2024 ChainSafe Systems (ON)
// SPDX-License-Identifier: LGPL-3.0-only

package triedb

import (
	"github.com/ChainSafe/gossamer/lib/common"
	"github.com/tidwall/btree"
)

type trieAccess interface {
	isTrieAccess()
}

type (
	encodedNodeAccess struct {
		hash        common.Hash
		encodedNode []byte
	}
	valueAccess struct {
		hash    common.Hash
		value   []byte
		fullKey []byte
	}
	inlineValueAccess struct {
		fullKey []byte
	}
	hashAccess struct {
		fullKey []byte
	}
	nonExistingNodeAccess struct {
		fullKey []byte
	}
)

func (encodedNodeAccess) isTrieAccess()     {}
func (valueAccess) isTrieAccess()           {}
func (inlineValueAccess) isTrieAccess()     {}
func (hashAccess) isTrieAccess()            {}
func (nonExistingNodeAccess) isTrieAccess() {}

type RecordedForKey int

const (
	RecordedValue RecordedForKey = iota
	RecordedHash
)

type RecordedNodesIterator struct {
	nodes []Record
	index int
}

func NewRecordedNodesIterator(nodes []Record) *RecordedNodesIterator {
	return &RecordedNodesIterator{nodes: nodes, index: -1}
}

func (r *RecordedNodesIterator) Next() *Record {
	if r.index < len(r.nodes)-1 {
		r.index++
		return &r.nodes[r.index]
	}
	return nil
}

func (r *RecordedNodesIterator) Peek() *Record {
	if r.index+1 < len(r.nodes)-1 {
		return &r.nodes[r.index+1]
	}
	return nil
}

type Record struct {
	Hash common.Hash
	Data []byte
}

type Recorder struct {
	nodes        []Record
	recordedKeys btree.Map[string, RecordedForKey]
}

func NewRecorder() *Recorder {
	return &Recorder{
		nodes:        []Record{},
		recordedKeys: *btree.NewMap[string, RecordedForKey](0),
	}
}

func (r *Recorder) record(access trieAccess) {
	switch a := access.(type) {
	case encodedNodeAccess:
		r.nodes = append(r.nodes, Record{Hash: a.hash, Data: a.encodedNode})
	case valueAccess:
		r.nodes = append(r.nodes, Record{Hash: a.hash, Data: a.value})
		r.recordedKeys.Set(string(a.fullKey), RecordedValue)
	case inlineValueAccess:
		r.recordedKeys.Set(string(a.fullKey), RecordedValue)
	case hashAccess:
		if _, ok := r.recordedKeys.Get(string(a.fullKey)); !ok {
			r.recordedKeys.Set(string(a.fullKey), RecordedHash)
		}
	case nonExistingNodeAccess:
		// We handle the non existing value/hash like having recorded the value
		r.recordedKeys.Set(string(a.fullKey), RecordedValue)
	}
}

func (r *Recorder) Drain() []Record {
	r.recordedKeys.Clear()
	nodes := r.nodes
	r.nodes = []Record{}
	return nodes
}
