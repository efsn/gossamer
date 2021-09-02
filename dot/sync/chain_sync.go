package sync

import (
	"math/big"
	"time"

	"github.com/libp2p/go-libp2p-core/peer"

	"github.com/ChainSafe/gossamer/dot/network"
	"github.com/ChainSafe/gossamer/dot/types"
	"github.com/ChainSafe/gossamer/lib/common"
)

const (
	// MAX_WORKERS is the maximum number of parallel sync workers
	// TODO: determine ideal value
	MAX_WORKERS      = 4
)

type (
	BlockRequestMessage  = network.BlockRequestMessage
	BlockResponseMessage = network.BlockResponseMessage
)

type chainSyncState uint64

var (
	bootstrap chainSyncState = 0
	idle      chainSyncState = 1
)

// ChainSync contains the methods used by the high-level service into the `chainSync` module
type ChainSync interface {
	// called upon receiving a BlockAnnounce
	setBlockAnnounce(from peer.ID, header *types.Header) error

	// called upon receiving a BlockAnnounceHandshake
	setPeerHead(p peer.ID, hash common.Hash, number *big.Int)

	// syncState returns the current syncing state
	syncState() chainSyncState
}

type chainSync struct {
	blockState BlockState
	network    Network

	// // TODO: put these in DisjointBlockSet?
	// bestSeenNumber *big.Int
	// bestSeenHash   common.Hash

	// queue of work created by setting peer heads
	workQueue chan *peerState

	// workers are put here when they are completed so we can handle their result
	resultQueue chan *worker

	// tracks the latest state we know of from our peers,
	// ie. their best block hash and number
	peerState map[peer.ID]*peerState

	// current workers that are attempting to obtain blocks
	nextWorker uint64
	workers    map[uint64]*worker

	// blocks which are ready to be processed are put into this channel
	// the `chainProcessor` will read from this channel and process the blocks
	// note: blocks must not be put into this channel unless their parent is known
	// TODO: channel or queue data structure?
	readyBlocks chan<- *types.BlockData

	// disjoint set of blocks which are known but not ready to be processed
	// ie. we only know the hash, number, or the parent block is unknown, or the body is unknown
	// note: the block may have empty fields, as some data about it may be unknown
	pendingBlocks DisjointBlockSet

	// bootstrap or idle (near-head)
	state chainSyncState
}

// peerState tracks our peers's best reported blocks
type peerState struct {
	who    peer.ID
	hash   common.Hash
	number *big.Int
}

// worker respresents a process that is attempting to sync from the specified start block to target block
// if it fails for some reason, `err` is set.
// otherwise, we can assume all the blocks have been received and added to the `readyBlocks` queue
type worker struct {
	id uint64

	startHash    common.Hash
	startNumber  *big.Int
	targetHash   common.Hash
	targetNumber *big.Int

	// TODO: add fields to request
	direction byte

	duration time.Duration
	err      *workerError
}

type workerError struct {
	err error
	who peer.ID // whose response caused the error, if any
}

func newChainSync(bs BlockState, net Network, readyBlocks chan<- *types.BlockData) *chainSync {
	return &chainSync{
		blockState:     bs,
		network:        net,
		//bestSeenNumber: big.NewInt(0),
		workQueue:      make(chan *peerState, 1024),
		resultQueue:    make(chan *worker, 1024),
		peerState:      make(map[peer.ID]*peerState),
		workers:        make(map[uint64]*worker),
		readyBlocks:    readyBlocks,
		pendingBlocks:  newDisjointBlockSet(),
		state:          bootstrap,
	}
}

func (cs *chainSync) syncState() chainSyncState {
	return cs.state
}

func (cs *chainSync) setBlockAnnounce(from peer.ID, header *types.Header) error {
	// check if we already know of this block, if not,
	// add to pendingBlocks set
	has, err := cs.blockState.HasHeader(header.Hash())
	if err != nil {
		return err
	}

	if has {
		return nil
	}

	cs.pendingBlocks.addHeader(header)
	return nil
}

// setPeerHead sets a peer's best known block and adds the peer's state to the workQueue
// to potentially trigger a worker
func (cs *chainSync) setPeerHead(p peer.ID, hash common.Hash, number *big.Int) {
	cs.peerState[p] = &peerState{
		hash:   hash,
		number: number,
	}

	// if number.Cmp(cs.bestSeenNumber) == 1 {
	// 	cs.bestSeenNumber = number
	// 	cs.bestSeenHash = hash
	// }

	cs.workQueue <- cs.peerState[p]
}

func (cs *chainSync) start() {
	// TODO: wait until we have received 5? peer heads
	go cs.sync()
}

func (cs *chainSync) sync() {
	ticker := time.NewTicker(time.Minute)

	for {
		select {
		case ps := <-cs.workQueue:
			// if a peer reports a greater head than us, or a chain which
			// appears to be a fork, begin syncing
			err := cs.handleWork(ps)
			if err != nil {
				logger.Error("failed to handle chain sync work", "error", err)
			}
		case res := <-cs.resultQueue:
			// delete worker from workers map
			delete(cs.workers, res.id)

			// handle results from worker
			// if there is an error, potentially retry the worker
			if res.err == nil {
				continue
			}

			logger.Error("worker error", "error", res.err.err)

			// TODO: new worker should update start block in case of bootstrap and re-dispatch
			// in case of idle, check pendingBlocks set again to determine new worker
			w := &worker{}
			cs.dispatchWorker(w)
		case <-ticker.C:
			// bootstrap complete, switch state to idle
			// and begin near-head fork-sync
		}

	}
}

// handleWork handles potential new work that may be triggered on receiving a peer's state
// in bootstrap mode, this begins the bootstrap process
// in idle mode, this adds the peer's state to the pendingBlocks set and potentially starts
// a fork sync
func (cs *chainSync) handleWork(ps *peerState) error {
	// if the peer reports a lower or equal best block number than us,
	// check if they are on a fork or not
	head, err := cs.blockState.BestBlockHeader()
	if err != nil {
		return err
	}

	if ps.number.Cmp(head.Number) <= 0 {
		// check if our block hash for that number is the same, if so, do nothing
		hash, err := cs.blockState.GetHashByNumber(ps.number)
		if err != nil {
			return err
		}

		if hash.Equal(ps.hash) {
			return nil
		}

		// check if their best block is on an invalid chain, if it is,
		// potentially downscore them
		// for now, we can remove them from the syncing peers set
		fin, err := cs.blockState.GetHighestFinalisedHeader()
		if err != nil {
			return err
		}

		// their block hash doesn't match ours for that number (ie. they are on a different
		// chain), and also the highest finalised block is higher than that number.
		// thus the peer is on an invalid chain
		if fin.Number.Cmp(ps.number) >= 0 {
			// TODO: downscore this peer, or temporarily don't sync from them?
			delete(cs.peerState, ps.who)
		}

		// TODO: peer is on a fork, add to pendingBlocks and begin fork request
		return nil
	}

	// the peer has a higher best block than us, add it to the disjoint block set
	cs.pendingBlocks.addHashAndNumber(ps.hash, ps.number)

	// if we already have the maximum number of workers, don't dispatch another
	if len(cs.workers) > MAX_WORKERS {
		return nil
	}

	// check current worker set for workers already working on these blocks
	// if there are none, dispatch new worker
	if cs.hasCurrentWorker(ps) {
		return nil
	}

	// TODO: this is for bootstrap mode, for idle fork-sync mode
	// we may want to reverse the direction
	worker := &worker{
		id:        cs.nextWorker,
		startHash: head.Hash(),
		startNumber: head.Number,
		targetHash: ps.hash,
		targetNumber: ps.number,
		direction: DIR_ASCENDING,
	}

	go cs.dispatchWorker(worker)
	return nil
}

// hasCurrentWorker returns whether the current workers cover the blocks reported by this peerState
func (cs *chainSync) hasCurrentWorker(ps *peerState) bool {
	for _, w := range cs.workers {
		if w.targetNumber.Cmp(ps.number) >= 0 {
			// there is some worker already syncing up until this number or further
			return true
		}
	}

	// if we're in bootstrap mode, and there already is a worker, we don't need to dispatch another
	if cs.state == bootstrap {
		return len(cs.workers) != 0
	}

	return false
}

// dispatchWorker begins making requests to the network and attempts to receive responses up until the target
// if it fails due to any reason, it sets the worker `err` and returns
// this function always places the worker into the `resultCh` for result handling upon return
func (cs *chainSync) dispatchWorker(w *worker) {
	// to deal with descending requests (ie. target may be lower than start) which are used in idle mode,
	// take absolute value of difference between start and target
	numBlocks := int(big.NewInt(0).Abs(big.NewInt(0).Sub(w.targetNumber, w.startNumber)).Int64())
	numRequests := numBlocks / MAX_RESPONSE_SIZE

	if numBlocks < MAX_RESPONSE_SIZE {
		numRequests = 1
	}

	start := time.Now()
	defer func() {
		end := time.Now()
		w.duration = end.Sub(start)
		cs.resultQueue <- w
	}()

	for i := 0; i < numRequests; i++ {
		req := &BlockRequestMessage{}
		err := cs.doSync(req)
		if err != nil {
			// failed to sync, set worker error and put into result queue
			w.err = err
			return
		}
	}
}

func (cs *chainSync) doSync(req *BlockRequestMessage) *workerError {
	// determine which peers have the blocks we want to request
	peers := cs.determineSyncPeers(req)

	// send out request and potentially receive response, error if timeout
	var (
		resp *BlockResponseMessage
		who peer.ID
	)

	for _, p := range peers {
		var err error
		resp, err = cs.network.DoBlockRequest(p, req)
		if err != nil {
			return &workerError{
				err: err,
				who: p,
			}
		}

		who = p
		break
	}

	// perform some pre-validation of response, error if failure
	if err := cs.validateResponse(resp); err != nil {
		return &workerError{
			err: err,
			who: who,
		}
	}

	return nil
}

// determineSyncPeers returns a list of peers that likely have the blocks in the given block request.
// TODO: implement this
func (cs *chainSync) determineSyncPeers(_ *BlockRequestMessage) []peer.ID {
	peers := []peer.ID{}
	for p := range cs.peerState {
		peers = append(peers, p)
	}
	return peers
}

// validateResponse performs pre-validation of a block response before placing it into either the 
// pendingBlocks or readyBlocks set.
// It checks the following:
// 	- the response is not empty
//  - the response contains all the expected fields
//  - each block has the correct parent, ie. the response constitutes a valid chain
func (cs *chainSync) validateResponse(resp *BlockResponseMessage) error {
	return nil
}