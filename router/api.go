// Copyright 2021 The Matrix.org Foundation C.I.C.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build !minimal
// +build !minimal

package router

import (
	"encoding/hex"
	"net"

	"github.com/Arceliar/phony"
	"github.com/matrix-org/pinecone/router/events"
	"github.com/matrix-org/pinecone/types"
)

type NeighbourInfo struct {
	PublicKey types.PublicKey
}

type PeerInfo struct {
	URI       string
	Port      int
	PublicKey string
	PeerType  int
	Zone      string
}

type NodeState struct {
	PeerID           string
	Connections      map[int]string
	Parent           string
	Coords           []uint64
	Announcement     types.SwitchAnnouncement
	AnnouncementTime uint64
	AscendingPeer    string
	AscendingPathID  string
	DescendingPeer   string
	DescendingPathID string
}

// Subscribe registers a subscriber to this node's events
func (r *Router) Subscribe(ch chan<- events.Event) NodeState {
	var stateCopy NodeState
	phony.Block(r, func() {
		r._subscribers[ch] = &phony.Inbox{}
		stateCopy.PeerID = r.public.String()
		connections := map[int]string{}
		for _, p := range r.state._peers {
			if p == nil {
				continue
			}
			connections[int(p.port)] = p.public.String()
		}
		stateCopy.Connections = connections
		parent := ""
		if r.state._parent != nil {
			parent = r.state._parent.public.String()
		}
		stateCopy.Parent = parent
		coords := []uint64{}
		for _, coord := range r.Coords() {
			coords = append(coords, uint64(coord))
		}
		stateCopy.Coords = coords
		announcement := r.state._rootAnnouncement()
		stateCopy.Announcement = announcement.SwitchAnnouncement
		stateCopy.AnnouncementTime = uint64(announcement.receiveTime.UnixNano())
		asc := ""
		ascPath := ""
		if r.state._ascending != nil {
			asc = r.state._ascending.PublicKey.String()
			ascPath = hex.EncodeToString(r.state._ascending.PathID[:])
		}
		stateCopy.AscendingPeer = asc
		stateCopy.AscendingPathID = ascPath
		desc := ""
		descPath := ""
		if r.state._descending != nil {
			desc = r.state._descending.PublicKey.String()
			descPath = hex.EncodeToString(r.state._descending.PathID[:])
		}
		stateCopy.DescendingPeer = desc
		stateCopy.DescendingPathID = descPath
	})
	return stateCopy
}

func (r *Router) Coords() types.Coordinates {
	return r.state.coords()
}

func (r *Router) Peers() []PeerInfo {
	var infos []PeerInfo
	phony.Block(r.state, func() {
		for _, p := range r.state._peers {
			if p == nil {
				continue
			}
			infos = append(infos, PeerInfo{
				URI:       string(p.uri),
				Port:      int(p.port),
				PublicKey: hex.EncodeToString(p.public[:]),
				PeerType:  int(p.peertype),
				Zone:      string(p.zone),
			})
		}
	})
	return infos
}

func (r *Router) NextHop(from net.Addr, frameType types.FrameType, dest net.Addr) net.Addr {
	var fromPeer *peer
	var nexthop net.Addr
	if from != nil {
		phony.Block(r.state, func() {
			fromPeer = r.state._lookupPeerForAddr(from)
		})

		if fromPeer == nil {
			r.log.Println("could not find peer info for previous peer")
			return nil
		}
	}

	var nextPeer *peer
	phony.Block(r.state, func() {
		nextPeer, _ = r.state._nextHopsFor(fromPeer, frameType, dest, types.VirtualSnakeWatermark{PublicKey: types.FullMask})
	})

	if nextPeer != nil {
		switch (dest).(type) {
		case types.Coordinates:
			var err error
			coords := types.Coordinates{}
			phony.Block(r.state, func() {
				coords, err = nextPeer._coords()
			})

			if err != nil {
				r.log.Println("failed retrieving coords for nexthop: %w")
				return nil
			}

			nexthop = coords
		case types.PublicKey:
			nexthop = nextPeer.public
		}
	}

	return nexthop
}
