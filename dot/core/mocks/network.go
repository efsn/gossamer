// Code generated by mockery v2.8.0. DO NOT EDIT.

package mocks

import (
	network "github.com/ChainSafe/gossamer/dot/network"
	"github.com/ChainSafe/gossamer/dot/peerset"
	"github.com/libp2p/go-libp2p-core/peer"
	mock "github.com/stretchr/testify/mock"
)

// MockNetwork is an autogenerated mock type for the Network type
type MockNetwork struct {
	mock.Mock
}

func (_m *MockNetwork) ReportPeer(_a0 peer.ID, _a1 peerset.ReputationChange) {
	_m.Called(_a0, _a1)
}

// GossipMessage provides a mock function with given fields: _a0
func (_m *MockNetwork) GossipMessage(_a0 network.NotificationsMessage) {
	_m.Called(_a0)
}
