package sync

import (
	"errors"
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/ChainSafe/gossamer/dot/network"
	syncmocks "github.com/ChainSafe/gossamer/dot/sync/mocks"
	"github.com/ChainSafe/gossamer/dot/types"
	"github.com/ChainSafe/gossamer/lib/common"
	"github.com/ChainSafe/gossamer/lib/common/optional"
	"github.com/ChainSafe/gossamer/lib/common/variadic"
	"github.com/ChainSafe/gossamer/lib/trie"

	"github.com/libp2p/go-libp2p-core/peer"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

var testTimeout = time.Second * 5

func newTestChainSync(t *testing.T) (*chainSync, *blockQueue) {
	header, err := types.NewHeader(common.NewHash([]byte{0}), trie.EmptyHash, trie.EmptyHash, big.NewInt(0), types.Digest{})
	require.NoError(t, err)

	bs := new(syncmocks.MockBlockState)
	bs.On("BestBlockHeader").Return(header, nil)
	bs.On("RegisterFinalizedChannel", mock.AnythingOfType("chan<- *types.FinalisationInfo")).Return(byte(0), nil)

	net := new(syncmocks.MockNetwork)
	net.On("DoBlockRequest", mock.AnythingOfType("peer.ID"), mock.AnythingOfType("*network.BlockRequestMessage")).Return(nil, nil)

	readyBlocks := newBlockQueue(MAX_RESPONSE_SIZE)
	cs, err := newChainSync(bs, net, readyBlocks)
	require.NoError(t, err)

	return cs, readyBlocks
}

func TestChainSync_SetPeerHead(t *testing.T) {
	cs, _ := newTestChainSync(t)

	testPeer := peer.ID("noot")
	hash := common.Hash{0xa, 0xb}
	number := big.NewInt(1000)
	err := cs.setPeerHead(testPeer, hash, number)
	require.NoError(t, err)

	expected := &peerState{
		hash:   hash,
		number: number,
	}
	require.Equal(t, expected, cs.peerState[testPeer])
	require.Equal(t, expected, <-cs.workQueue)
	require.True(t, cs.pendingBlocks.hasBlock(hash))

	// test case where peer has a lower head than us, but they are on the same chain as us
	cs.blockState = new(syncmocks.MockBlockState)
	header, err := types.NewHeader(common.NewHash([]byte{0}), trie.EmptyHash, trie.EmptyHash, big.NewInt(1000), types.Digest{})
	require.NoError(t, err)
	cs.blockState.(*syncmocks.MockBlockState).On("BestBlockHeader").Return(header, nil)
	fin, err := types.NewHeader(common.NewHash([]byte{0}), trie.EmptyHash, trie.EmptyHash, big.NewInt(998), types.Digest{})
	require.NoError(t, err)
	cs.blockState.(*syncmocks.MockBlockState).On("GetHighestFinalisedHeader").Return(fin, nil)
	cs.blockState.(*syncmocks.MockBlockState).On("GetHashByNumber", mock.AnythingOfType("*big.Int")).Return(hash, nil)

	number = big.NewInt(999)
	err = cs.setPeerHead(testPeer, hash, number)
	require.NoError(t, err)
	expected = &peerState{
		hash:   hash,
		number: number,
	}
	require.Equal(t, expected, cs.peerState[testPeer])
	select {
	case <-cs.workQueue:
		t.Fatal("should not put chain we already have into work queue")
	default:
	}

	// test case where peer has a lower head than us, and they are on an invalid fork
	cs.blockState = new(syncmocks.MockBlockState)
	cs.blockState.(*syncmocks.MockBlockState).On("BestBlockHeader").Return(header, nil)
	fin, err = types.NewHeader(common.NewHash([]byte{0}), trie.EmptyHash, trie.EmptyHash, big.NewInt(1000), types.Digest{})
	require.NoError(t, err)
	cs.blockState.(*syncmocks.MockBlockState).On("GetHighestFinalisedHeader").Return(fin, nil)
	cs.blockState.(*syncmocks.MockBlockState).On("GetHashByNumber", mock.AnythingOfType("*big.Int")).Return(common.Hash{}, nil)

	number = big.NewInt(999)
	err = cs.setPeerHead(testPeer, hash, number)
	require.True(t, errors.Is(err, errPeerOnInvalidFork))
	expected = &peerState{
		hash:   hash,
		number: number,
	}
	require.Equal(t, expected, cs.peerState[testPeer])
	select {
	case <-cs.workQueue:
		t.Fatal("should not put invalid fork into work queue")
	default:
	}

	// test case where peer has a lower head than us, but they are on a valid fork (that is not our chain)
	cs.blockState = new(syncmocks.MockBlockState)
	cs.blockState.(*syncmocks.MockBlockState).On("BestBlockHeader").Return(header, nil)
	fin, err = types.NewHeader(common.NewHash([]byte{0}), trie.EmptyHash, trie.EmptyHash, big.NewInt(998), types.Digest{})
	require.NoError(t, err)
	cs.blockState.(*syncmocks.MockBlockState).On("GetHighestFinalisedHeader").Return(fin, nil)
	cs.blockState.(*syncmocks.MockBlockState).On("GetHashByNumber", mock.AnythingOfType("*big.Int")).Return(common.Hash{}, nil)
	cs.blockState.(*syncmocks.MockBlockState).On("HasHeader", mock.AnythingOfType("common.Hash")).Return(true, nil)

	number = big.NewInt(999)
	err = cs.setPeerHead(testPeer, hash, number)
	require.NoError(t, err)
	expected = &peerState{
		hash:   hash,
		number: number,
	}
	require.Equal(t, expected, cs.peerState[testPeer])
	select {
	case <-cs.workQueue:
		t.Fatal("should not put fork we already have into work queue")
	default:
	}
}

func TestChainSync_sync_bootstrap_withWorkerError(t *testing.T) {
	cs, _ := newTestChainSync(t)

	go cs.sync()
	defer cs.cancel()

	testPeer := peer.ID("noot")
	cs.peerState[testPeer] = &peerState{
		number: big.NewInt(1000),
	}

	cs.workQueue <- cs.peerState[testPeer]

	select {
	case res := <-cs.resultQueue:
		expected := &workerError{
			err: errNilResponse, // since MockNetwork returns a nil response
			who: testPeer,
		}
		require.Equal(t, expected, res.err)
	case <-time.After(testTimeout):
		t.Fatal("did not get worker response")
	}

	require.Equal(t, bootstrap, cs.state)
}

func TestChainSync_sync_tip(t *testing.T) {
	cs, _ := newTestChainSync(t)
	cs.blockState = new(syncmocks.MockBlockState)
	header, err := types.NewHeader(common.NewHash([]byte{0}), trie.EmptyHash, trie.EmptyHash, big.NewInt(1000), types.Digest{})
	require.NoError(t, err)
	cs.blockState.(*syncmocks.MockBlockState).On("BestBlockHeader").Return(header, nil)

	go cs.sync()
	defer cs.cancel()

	testPeer := peer.ID("noot")
	cs.peerState[testPeer] = &peerState{
		number: big.NewInt(999),
	}

	cs.workQueue <- cs.peerState[testPeer]
	time.Sleep(time.Second)
	require.Equal(t, tip, cs.state)
}

func TestChainSync_getTarget(t *testing.T) {
	cs, _ := newTestChainSync(t)
	require.Equal(t, big.NewInt(2<<32-1), cs.getTarget())

	cs.peerState = map[peer.ID]*peerState{
		"testA": {
			number: big.NewInt(1000),
		},
	}

	require.Equal(t, big.NewInt(1000), cs.getTarget())

	cs.peerState = map[peer.ID]*peerState{
		"testA": {
			number: big.NewInt(1000),
		},
		"testB": {
			number: big.NewInt(2000),
		},
	}

	require.Equal(t, big.NewInt(1500), cs.getTarget())
}

func TestWorkerToRequests(t *testing.T) {
	_, err := workerToRequests(&worker{})
	require.Equal(t, errWorkerMissingStartNumber, err)

	w := &worker{
		startNumber: big.NewInt(1),
	}
	_, err = workerToRequests(w)
	require.Equal(t, errWorkerMissingTargetNumber, err)

	w = &worker{
		startNumber:  big.NewInt(10),
		targetNumber: big.NewInt(1),
		direction:    DIR_ASCENDING,
	}
	_, err = workerToRequests(w)
	require.Equal(t, errInvalidDirection, err)

	type testCase struct {
		w        *worker
		expected []*BlockRequestMessage
	}

	testCases := []testCase{
		{
			w: &worker{
				startNumber:  big.NewInt(1),
				targetNumber: big.NewInt(1 + MAX_RESPONSE_SIZE),
				direction:    DIR_ASCENDING,
				requestData:  bootstrapRequestData,
			},
			expected: []*BlockRequestMessage{
				{
					RequestedData: bootstrapRequestData,
					StartingBlock: variadic.MustNewUint64OrHash(1),
					EndBlockHash:  optional.NewHash(false, common.Hash{}),
					Direction:     DIR_ASCENDING,
					Max:           optional.NewUint32(true, uint32(128)),
				},
			},
		},
		{
			w: &worker{
				startNumber:  big.NewInt(1),
				targetNumber: big.NewInt(1 + (MAX_RESPONSE_SIZE * 2)),
				direction:    DIR_ASCENDING,
				requestData:  bootstrapRequestData,
			},
			expected: []*BlockRequestMessage{
				{
					RequestedData: bootstrapRequestData,
					StartingBlock: variadic.MustNewUint64OrHash(1),
					EndBlockHash:  optional.NewHash(false, common.Hash{}),
					Direction:     DIR_ASCENDING,
					Max:           optional.NewUint32(false, 0),
				},
				{
					RequestedData: network.RequestedDataHeader + network.RequestedDataBody + network.RequestedDataJustification,
					StartingBlock: variadic.MustNewUint64OrHash(1 + MAX_RESPONSE_SIZE),
					EndBlockHash:  optional.NewHash(false, common.Hash{}),
					Direction:     DIR_ASCENDING,
					Max:           optional.NewUint32(true, uint32(128)),
				},
			},
		},
		{
			w: &worker{
				startNumber:  big.NewInt(1),
				targetNumber: big.NewInt(10),
				direction:    DIR_ASCENDING,
				requestData:  bootstrapRequestData,
			},
			expected: []*BlockRequestMessage{
				{
					RequestedData: bootstrapRequestData,
					StartingBlock: variadic.MustNewUint64OrHash(1),
					EndBlockHash:  optional.NewHash(false, common.Hash{}),
					Direction:     DIR_ASCENDING,
					Max:           optional.NewUint32(true, 9),
				},
			},
		},
		{
			w: &worker{
				startNumber:  big.NewInt(10),
				targetNumber: big.NewInt(1),
				direction:    DIR_DESCENDING,
				requestData:  bootstrapRequestData,
			},
			expected: []*BlockRequestMessage{
				{
					RequestedData: bootstrapRequestData,
					StartingBlock: variadic.MustNewUint64OrHash(10),
					EndBlockHash:  optional.NewHash(false, common.Hash{}),
					Direction:     DIR_DESCENDING,
					Max:           optional.NewUint32(true, 9),
				},
			},
		},
		{
			w: &worker{
				startNumber:  big.NewInt(1),
				targetNumber: big.NewInt(1 + MAX_RESPONSE_SIZE + (MAX_RESPONSE_SIZE / 2)),
				direction:    DIR_ASCENDING,
				requestData:  bootstrapRequestData,
			},
			expected: []*BlockRequestMessage{
				{
					RequestedData: bootstrapRequestData,
					StartingBlock: variadic.MustNewUint64OrHash(1),
					EndBlockHash:  optional.NewHash(false, common.Hash{}),
					Direction:     DIR_ASCENDING,
					Max:           optional.NewUint32(false, 0),
				},
				{
					RequestedData: network.RequestedDataHeader + network.RequestedDataBody + network.RequestedDataJustification,
					StartingBlock: variadic.MustNewUint64OrHash(1 + MAX_RESPONSE_SIZE),
					EndBlockHash:  optional.NewHash(false, common.Hash{}),
					Direction:     DIR_ASCENDING,
					Max:           optional.NewUint32(true, uint32(MAX_RESPONSE_SIZE/2)),
				},
			},
		},
		{
			w: &worker{
				startNumber:  big.NewInt(1),
				targetNumber: big.NewInt(10),
				targetHash:   common.Hash{0xa},
				direction:    DIR_ASCENDING,
				requestData:  bootstrapRequestData,
			},
			expected: []*BlockRequestMessage{
				{
					RequestedData: bootstrapRequestData,
					StartingBlock: variadic.MustNewUint64OrHash(1),
					EndBlockHash:  optional.NewHash(true, common.Hash{0xa}),
					Direction:     DIR_ASCENDING,
					Max:           optional.NewUint32(true, 9),
				},
			},
		},
		{
			w: &worker{
				startNumber:  big.NewInt(1),
				startHash:    common.Hash{0xb},
				targetNumber: big.NewInt(10),
				targetHash:   common.Hash{0xc},
				direction:    DIR_ASCENDING,
				requestData:  bootstrapRequestData,
			},
			expected: []*BlockRequestMessage{
				{
					RequestedData: bootstrapRequestData,
					StartingBlock: variadic.MustNewUint64OrHash(common.Hash{0xb}),
					EndBlockHash:  optional.NewHash(true, common.Hash{0xc}),
					Direction:     DIR_ASCENDING,
					Max:           optional.NewUint32(true, 9),
				},
			},
		},
	}

	for i, tc := range testCases {
		reqs, err := workerToRequests(tc.w)
		require.NoError(t, err, fmt.Sprintf("case %d failed", i))
		require.Equal(t, len(tc.expected), len(reqs), fmt.Sprintf("case %d failed", i))
		require.Equal(t, tc.expected, reqs, fmt.Sprintf("case %d failed", i))
	}
}

func TestValidateBlockData(t *testing.T) {
	req := &BlockRequestMessage{
		RequestedData: bootstrapRequestData,
	}

	err := validateBlockData(req, nil)
	require.Equal(t, errNilBlockData, err)

	err = validateBlockData(req, &types.BlockData{})
	require.Equal(t, errNilHeaderInResponse, err)

	err = validateBlockData(req, &types.BlockData{
		Header: &optional.Header{},
	})
	require.Equal(t, errNilBodyInResponse, err)

	err = validateBlockData(req, &types.BlockData{
		Header: &optional.Header{},
		Body:   &optional.Body{},
	})
	require.NoError(t, err)
}

func TestChainSync_validateResponse(t *testing.T) {
	cs, _ := newTestChainSync(t)
	err := cs.validateResponse(nil, nil)
	require.Equal(t, errEmptyBlockData, err)

	req := &BlockRequestMessage{
		RequestedData: network.RequestedDataHeader,
	}

	resp := &BlockResponseMessage{
		BlockData: []*types.BlockData{
			{
				Header: (&types.Header{
					Number: big.NewInt(1),
				}).AsOptional(),
				Body: (&types.Body{}).AsOptional(),
			},
			{
				Header: (&types.Header{
					Number: big.NewInt(2),
				}).AsOptional(),
				Body: (&types.Body{}).AsOptional(),
			},
		},
	}

	hash := (&types.Header{
		Number: big.NewInt(2),
	}).Hash()
	err = cs.validateResponse(req, resp)
	require.Equal(t, errResponseIsNotChain, err)
	require.True(t, cs.pendingBlocks.hasBlock(hash))
	cs.pendingBlocks.removeBlock(hash)

	parent := (&types.Header{
		Number: big.NewInt(1),
	}).Hash()
	resp = &BlockResponseMessage{
		BlockData: []*types.BlockData{
			{
				Header: (&types.Header{
					Number: big.NewInt(1),
				}).AsOptional(),
				Body: (&types.Body{}).AsOptional(),
			},
			{
				Header: (&types.Header{
					ParentHash: parent,
					Number:     big.NewInt(3),
				}).AsOptional(),
				Body: (&types.Body{}).AsOptional(),
			},
		},
	}

	hash = (&types.Header{
		ParentHash: parent,
		Number:     big.NewInt(3),
	}).Hash()
	err = cs.validateResponse(req, resp)
	require.Equal(t, errResponseIsNotChain, err)
	require.True(t, cs.pendingBlocks.hasBlock(hash))
	cs.pendingBlocks.removeBlock(hash)

	parent = (&types.Header{
		Number: big.NewInt(2),
	}).Hash()
	resp = &BlockResponseMessage{
		BlockData: []*types.BlockData{
			{
				Header: (&types.Header{
					Number: big.NewInt(2),
				}).AsOptional(),
				Body: (&types.Body{}).AsOptional(),
			},
			{
				Header: (&types.Header{
					ParentHash: parent,
					Number:     big.NewInt(3),
				}).AsOptional(),
				Body: (&types.Body{}).AsOptional(),
			},
		},
	}

	err = cs.validateResponse(req, resp)
	require.NoError(t, err)
	require.False(t, cs.pendingBlocks.hasBlock(hash))
}

func TestChainSync_doSync(t *testing.T) {
	cs, readyBlocks := newTestChainSync(t)

	req := &BlockRequestMessage{
		RequestedData: bootstrapRequestData,
		StartingBlock: variadic.MustNewUint64OrHash(1),
		EndBlockHash:  optional.NewHash(false, common.Hash{}),
		Direction:     DIR_ASCENDING,
		Max:           optional.NewUint32(true, uint32(1)),
	}

	workerErr := cs.doSync(req)
	require.NotNil(t, workerErr)
	require.Equal(t, errNoPeers, workerErr.err)

	cs.peerState["noot"] = &peerState{
		number: big.NewInt(100),
	}

	workerErr = cs.doSync(req)
	require.NotNil(t, workerErr)
	require.Equal(t, errNilResponse, workerErr.err)

	resp := &BlockResponseMessage{
		BlockData: []*types.BlockData{
			{
				Header: (&types.Header{
					Number: big.NewInt(1),
				}).AsOptional(),
				Body: (&types.Body{}).AsOptional(),
			},
		},
	}

	cs.network = new(syncmocks.MockNetwork)
	cs.network.(*syncmocks.MockNetwork).On("DoBlockRequest", mock.AnythingOfType("peer.ID"), mock.AnythingOfType("*network.BlockRequestMessage")).Return(resp, nil)

	workerErr = cs.doSync(req)
	require.Nil(t, workerErr)
	bd := readyBlocks.pop()
	require.NotNil(t, bd)
	require.Equal(t, resp.BlockData[0], bd)

	parent := (&types.Header{
		Number: big.NewInt(2),
	}).Hash()
	resp = &BlockResponseMessage{
		BlockData: []*types.BlockData{
			{
				Header: (&types.Header{
					ParentHash: parent,
					Number:     big.NewInt(3),
				}).AsOptional(),
				Body: (&types.Body{}).AsOptional(),
			},
			{
				Header: (&types.Header{
					Number: big.NewInt(2),
				}).AsOptional(),
				Body: (&types.Body{}).AsOptional(),
			},
		},
	}

	// test to see if descending blocks get reversed
	req.Direction = DIR_DESCENDING
	cs.network = new(syncmocks.MockNetwork)
	cs.network.(*syncmocks.MockNetwork).On("DoBlockRequest", mock.AnythingOfType("peer.ID"), mock.AnythingOfType("*network.BlockRequestMessage")).Return(resp, nil)
	workerErr = cs.doSync(req)
	require.Nil(t, workerErr)

	bd = readyBlocks.pop()
	require.NotNil(t, bd)
	require.Equal(t, resp.BlockData[0], bd)

	bd = readyBlocks.pop()
	require.NotNil(t, bd)
	require.Equal(t, resp.BlockData[1], bd)
}

func TestHandleReadyBlock(t *testing.T) {
	cs, readyBlocks := newTestChainSync(t)

	// test that descendant chain gets returned by getReadyDescendants on block 1 being ready
	header1 := &types.Header{
		Number: big.NewInt(1),
	}
	block1 := &types.Block{
		Header: header1,
		Body:   &types.Body{},
	}

	header2 := &types.Header{
		ParentHash: header1.Hash(),
		Number:     big.NewInt(2),
	}
	block2 := &types.Block{
		Header: header2,
		Body:   &types.Body{},
	}
	cs.pendingBlocks.addBlock(block2)

	header3 := &types.Header{
		ParentHash: header2.Hash(),
		Number:     big.NewInt(3),
	}
	block3 := &types.Block{
		Header: header3,
		Body:   &types.Body{},
	}
	cs.pendingBlocks.addBlock(block3)

	header2NotDescendant := &types.Header{
		ParentHash: common.Hash{0xff},
		Number:     big.NewInt(2),
	}
	block2NotDescendant := &types.Block{
		Header: header2NotDescendant,
		Body:   &types.Body{},
	}
	cs.pendingBlocks.addBlock(block2NotDescendant)

	handleReadyBlock(block1.ToBlockData(), cs.pendingBlocks, cs.readyBlocks)

	require.False(t, cs.pendingBlocks.hasBlock(header1.Hash()))
	require.False(t, cs.pendingBlocks.hasBlock(header2.Hash()))
	require.False(t, cs.pendingBlocks.hasBlock(header3.Hash()))
	require.True(t, cs.pendingBlocks.hasBlock(header2NotDescendant.Hash()))

	require.Equal(t, block1.ToBlockData(), readyBlocks.pop())
	require.Equal(t, block2.ToBlockData(), readyBlocks.pop())
	require.Equal(t, block3.ToBlockData(), readyBlocks.pop())
}