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

package router

import (
	"encoding/hex"
	"fmt"
	"math"
	"time"

	"github.com/Arceliar/phony"
	"github.com/matrix-org/pinecone/types"
)

// announcementInterval is the frequency at which this
// node will send root announcements to other peers.
const announcementInterval = time.Minute * 15

// announcementTimeout is the amount of time that must
// pass without receiving a root announcement before we
// will assume that the peer is dead.
const announcementTimeout = announcementInterval * 2

func (s *state) _maintainTree() {
	select {
	case <-s.r.context.Done():
		return
	default:
	}

	if s._parent == nil {
		s._sequence++
		s._sendTreeAnnouncements()
	}

	s._maintainTreeIn(announcementInterval)
}

type rootAnnouncementWithTime struct {
	types.SwitchAnnouncement
	receiveTime  time.Time // when did we receive the update?
	receiveOrder uint64    // the relative order that the update was received
}

func (a *rootAnnouncementWithTime) forPeer(p *peer) *types.Frame {
	if p == nil || p.port == 0 {
		return nil
	}
	announcement := a.SwitchAnnouncement
	announcement.Signatures = append([]types.SignatureWithHop{}, a.Signatures...)
	for _, sig := range announcement.Signatures {
		if p.router.public.EqualTo(sig.PublicKey) {
			// For some reason the announcement that we want to send already
			// includes our signature. This shouldn't really happen but if we
			// did send it, other nodes would end up ignoring the announcement
			// anyway since it would appear to be a routing loop.
			return nil
		}
	}
	// Sign the announcement.
	if err := announcement.Sign(p.router.private[:], p.port); err != nil {
		p.router.log.Println("Failed to sign switch announcement:", err)
		return nil
	}
	frame := &types.Frame{
		Type:    types.TypeSTP,
		Payload: make([]byte, types.MaxPayloadSize),
	}
	n, err := announcement.MarshalBinary(frame.Payload[:])
	if err != nil {
		p.router.log.Println("Failed to marshal switch announcement:", err)
		return nil
	}
	frame.Payload = frame.Payload[:n]
	return frame
}

func (s *state) _rootAnnouncement() *rootAnnouncementWithTime {
	if s._parent == nil || s._announcements[s._parent] == nil {
		return &rootAnnouncementWithTime{
			SwitchAnnouncement: types.SwitchAnnouncement{
				RootPublicKey: s.r.public,
				Sequence:      types.Varu64(s._sequence),
			},
		}
	}
	return s._announcements[s._parent]
}

func (s *state) coords() types.SwitchPorts {
	var coords types.SwitchPorts
	phony.Block(s, func() {
		coords = s._coords()
	})
	return coords
}

func (s *state) _coords() types.SwitchPorts {
	if ann := s._rootAnnouncement(); ann != nil {
		return ann.Coords()
	}
	return types.SwitchPorts{}
}

func (s *state) _becomeRoot() {
	if s._parent == nil {
		return
	}
	s._parent = nil
	s._maintainTree()
}

func (s *state) sendTreeAnnouncementToPeer(ann *rootAnnouncementWithTime, p *peer) {
	if peerAnn := ann.forPeer(p); peerAnn != nil {
		p.proto.push(peerAnn)
	}
}

func (s *state) _sendTreeAnnouncements() {
	ann := s._rootAnnouncement()
	for _, p := range s._peers {
		if p == nil || !p.started.Load() {
			continue
		}
		s.sendTreeAnnouncementToPeer(ann, p)
	}
}

func (s *state) _nextHopsTree(from *peer, f *types.Frame) *peer {
	// We'll collect all possible candidates. We start at PortCount-1
	// because that guarantees the last candidate port is always 0, so
	// that if we don't know what else to do with a packet, we hand it
	// up to the local router.
	var bestPeer *peer
	newCandidate := func(peer *peer) {
		bestPeer = peer
	}

	// If it's loopback then don't bother doing anything else.
	ourCoords := s._coords()
	ourRoot := s._rootAnnouncement()
	if f.Destination.EqualTo(ourCoords) {
		return s.r.local
	}

	// Work out how close our own coordinates are to the destination
	// message. This is important because we'll only forward a frame
	// to a peer that takes the message closer to the destination than
	// we are.
	ourDist := int64(ourCoords.DistanceTo(f.Destination))
	if ourDist == 0 {
		// It's impossible to get closer so there's a pretty good
		// chance at this point that the traffic is destined for us.
		// Pass it up to the router.
		return s.r.local
	}

	// Now work out which of our peers takes the message closer.
	bestDist := ourDist
	for p, ann := range s._announcements {
		switch {
		case !p.started.Load():
			continue
		case ann == nil:
			continue
		case p == from:
			continue
		case ourRoot.RootPublicKey != ann.RootPublicKey:
			continue
		case ourRoot.Sequence != ann.Sequence:
			continue
		}

		// Look up the coordinates of the peer, and the distance
		// across the tree to those coordinates.
		peerCoords := ann.PeerCoords()
		peerDist := int64(peerCoords.DistanceTo(f.Destination))
		switch {
		case peerDist == 0 || f.Destination.EqualTo(peerCoords):
			// The peer is the actual destination.
			return p

		case peerDist < bestDist:
			// The peer is closer to the destination.
			bestDist = peerDist
			newCandidate(p)
		}
	}

	// If we've got an eligible next peer, and it doesn't create a
	// routing loop by sending the frame back where it came from,
	// then return it.
	return bestPeer
}

func (s *state) _handleTreeAnnouncement(p *peer, f *types.Frame) error {
	var newUpdate types.SwitchAnnouncement
	if _, err := newUpdate.UnmarshalBinary(f.Payload); err != nil {
		return fmt.Errorf("update unmarshal failed: %w", err)
	}

	if len(newUpdate.Signatures) == 0 {
		// The update must have signatures.
		return fmt.Errorf("update has no signatures")
	}
	sigs := make(map[string]struct{})
	for index, sig := range newUpdate.Signatures {
		if index == 0 && sig.PublicKey != newUpdate.RootPublicKey {
			return fmt.Errorf("update first signature doesn't match root key")
		}
		if sig.Hop == 0 {
			return fmt.Errorf("update contains invalid 0 hop")
		}
		if index == len(newUpdate.Signatures)-1 && p.public != sig.PublicKey {
			return fmt.Errorf("update last signature is not from direct peer")
		}
		pk := hex.EncodeToString(sig.PublicKey[:])
		if _, ok := sigs[pk]; ok {
			return fmt.Errorf("update contains routing loop")
		}
		sigs[pk] = struct{}{}
	}

	if ann := s._announcements[p]; ann != nil {
		if newUpdate.RootPublicKey == ann.RootPublicKey && newUpdate.Sequence < ann.Sequence {
			return fmt.Errorf("update replays old sequence number")
		}
	}

	lastParentUpdate := s._rootAnnouncement()
	lastRootKey := s.r.public
	if lastParentUpdate != nil {
		lastRootKey = lastParentUpdate.RootPublicKey
	}
	rootDelta := newUpdate.RootPublicKey.CompareTo(lastRootKey)

	// Save the root announcement against the peer.
	s._ordering++
	s._announcements[p] = &rootAnnouncementWithTime{
		SwitchAnnouncement: newUpdate,
		receiveTime:        time.Now(),
		receiveOrder:       s._ordering,
	}

	if p == s._parent {
		if s._waiting {
			return fmt.Errorf("invalid update from parent whilst waiting to re-parent")
		}

		switch {
		case rootDelta < 0:
			fallthrough
		case rootDelta == 0 && newUpdate.Sequence <= lastParentUpdate.Sequence:
			s._waiting = true
			s._becomeRoot()

			time.AfterFunc(time.Second, func() {
				s.Act(nil, func() {
					s._waiting = false
					if s._selectNewParent() {
						s._bootstrapNow()
					}
				})
			})

		case rootDelta == 0 && newUpdate.Sequence > lastParentUpdate.Sequence:
			fallthrough
		case rootDelta > 0:
			s._sendTreeAnnouncements()
		}
	} else {
		if rootDelta < 0 && !s._waiting {
			s.sendTreeAnnouncementToPeer(lastParentUpdate, p)
			return nil
		}
	}
	if !s._waiting {
		if s._selectNewParent() {
			s._bootstrapNow()
		}
	}

	return nil
}

func (s *state) _selectNewParent() bool {
	root := s._rootAnnouncement()
	bestKey := root.RootPublicKey
	bestSeq := root.Sequence
	if bestKey.CompareTo(s.r.public) < 0 {
		bestKey = s.r.public
		bestSeq = 0
	}
	bestOrder := uint64(math.MaxUint64)
	var bestPeer *peer

	for peer, ann := range s._announcements {
		if !peer.started.Load() {
			continue
		}
		if ann == nil || time.Since(ann.receiveTime) >= announcementTimeout {
			continue
		}
		accept := func() {
			bestKey = ann.RootPublicKey
			bestPeer = peer
			bestOrder = ann.receiveOrder
			bestSeq = ann.Sequence
		}
		keyDelta := ann.RootPublicKey.CompareTo(bestKey)
		switch {
		case ann.IsLoopOrChildOf(s.r.public):
			// ignore our children or loopy announcements
		case keyDelta > 0:
			accept()
		case keyDelta < 0:
			// ignore weaker root keys
		case ann.Sequence > bestSeq:
			accept()
		case ann.Sequence < bestSeq:
			// ignore lower sequence numbers
		case ann.receiveOrder < bestOrder:
			// otherwise, pick the parent that sent us the latest root
			// update first, for the lower latency path to the root
			accept()
		}
	}

	if bestPeer != nil {
		// Only send tree announcements if the parent actually changed.
		if bestPeer != s._parent {
			s._parent = bestPeer
			s._sendTreeAnnouncements()
			return true
		}
		return false
	}

	// No suitable other peer was found, so we'll just become the root
	// and hope that one of our peers corrects us if it matters.
	s._becomeRoot()
	return false
}

func (s *state) _ancestors() ([]types.PublicKey, *peer) {
	root, parent := s._rootAnnouncement(), s._parent
	if parent == nil {
		return nil, nil
	}
	ancestors := make([]types.PublicKey, 0, 1+len(root.Signatures))
	if len(root.Signatures) == 0 {
		return ancestors, parent
	}
	for _, sig := range root.Signatures {
		ancestors = append(ancestors, sig.PublicKey)
	}
	return ancestors, parent
}
