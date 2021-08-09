// Copyright 2019 ChainSafe Systems (ON) Corp.
// This file is part of gossamer.
//
// The gossamer library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The gossamer library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the gossamer library. If not, see <http://www.gnu.org/licenses/>.

package types

import (
	"github.com/ChainSafe/gossamer/lib/crypto/sr25519"
	"github.com/ChainSafe/gossamer/lib/keystore"
	"github.com/ChainSafe/gossamer/pkg/scale"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestEncode(t *testing.T) {
	//expData := common.MustHexToBytes("0x010801d43593c715fdd31c61141abd04a99fd6822c8558854ccde39a5684e7a56da27d0100000000000000018eaf04151687736326c9fea17e25fc5287613693c912909cb226aa4794f26a4801000000000000004d58630000000000000000000000000000000000000000000000000000000000")

	keyring, err := keystore.NewSr25519Keyring()
	require.NoError(t, err)

	authA := &AuthorityRaw{
		Key:    keyring.Alice().Public().(*sr25519.PublicKey).AsBytes(),
		Weight: 1,
	}

	authB := &AuthorityRaw{
		Key:    keyring.Bob().Public().(*sr25519.PublicKey).AsBytes(),
		Weight: 1,
	}

	auth1 := AuthorityRaw{
		Key:    keyring.Alice().Public().(*sr25519.PublicKey).AsBytes(),
		Weight: 1,
	}

	auth2 := AuthorityRaw{
		Key:    keyring.Bob().Public().(*sr25519.PublicKey).AsBytes(),
		Weight: 1,
	}

	var vdt = BabeConsensusDigest
	err = vdt.Set(NextEpochDataNew{
		Authorities: []AuthorityRaw{auth1, auth2},
		Randomness:  [32]byte{77, 88, 99},
	})

	digest := NextEpochData{
		Authorities: []*AuthorityRaw{authA, authB},
		Randomness:  [32]byte{77, 88, 99},
	}

	//digestNew := NextEpochDataNew{
	//	Authorities: []AuthorityRaw{auth1, auth2},
	//	Randomness:  [32]byte{77, 88, 99},
	//}

	data, err := digest.Encode()
	require.NoError(t, err)

	enc, err := scale.Marshal(vdt)
	require.NoError(t, err)
	require.Equal(t, data, enc )

}

