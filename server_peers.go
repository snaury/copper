package copper

import (
	"fmt"
	"io"
	"log"
	"net"
	"time"

	"github.com/snaury/copper/raw"
)

type serverPeerKey struct {
	network string
	address string
}

type serverPeerRemote struct {
	peer     *serverPeer
	client   *clientConn
	targetID int64
	name     string
	settings PublishSettings

	subscriptions map[*serverSubscription]struct{}
}

var _ endpointReference = &serverPeerRemote{}

func (remote *serverPeerRemote) getEndpointsLocked() []Endpoint {
	if peer := remote.peer; peer != nil {
		return []Endpoint{{
			Network:  peer.key.network,
			Address:  peer.key.address,
			TargetID: remote.targetID,
		}}
	}
	return nil
}

func (remote *serverPeerRemote) handleRequestLocked(client raw.Stream) handleRequestStatus {
	if peer := remote.peer; peer != nil {
		peer.owner.lock.Unlock()
		defer peer.owner.lock.Lock()
		stream, err := remote.client.Open(remote.targetID)
		if err != nil {
			return handleRequestStatusFailure
		}
		passthruBoth(client, stream)
		return handleRequestStatusDone
	}
	return handleRequestStatusNoRoute
}

type serverPeer struct {
	owner    *server
	key      serverPeerKey
	distance uint32

	failure error
	failed  chan struct{}
	client  *clientConn

	remotesByTarget map[int64]*serverPeerRemote
	remotesByName   map[string]map[int64]*serverPeerRemote
}

func (s *server) addPeerLocked(network, address string, distance uint32) error {
	key := serverPeerKey{
		network: network,
		address: address,
	}
	peer := s.peers[key]
	if peer != nil {
		return fmt.Errorf("peer for %s:%s already exists", network, address)
	}
	peer = &serverPeer{
		owner:    s,
		key:      key,
		distance: distance,

		failed: make(chan struct{}),

		remotesByTarget: make(map[int64]*serverPeerRemote),
		remotesByName:   make(map[string]map[int64]*serverPeerRemote),
	}
	s.peers[key] = peer
	go peer.connectloop()
	return nil
}

func (peer *serverPeer) closeWithErrorLocked(err error) {
	if peer.failure == nil {
		peer.failure = err
		close(peer.failed)
	}
}

func (peer *serverPeer) sleep(d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-peer.failed:
		return false
	}
}

func (peer *serverPeer) connectloop() {
	for {
		select {
		case <-peer.failed:
			return
		default:
		}
		conn, err := net.Dial(peer.key.network, peer.key.address)
		if err != nil {
			if peer.sleep(5 * time.Second) {
				continue
			}
			return
		}
		client := newClient(conn)
		peer.attachClient(client)
		stop := make(chan struct{})
		go func() {
			select {
			case <-peer.failed:
				client.Close()
			case <-stop:
			}
		}()
		client.Serve()
		peer.detachClient(client)
		client.Close()
		close(stop)
	}
}

func (peer *serverPeer) attachClient(client *clientConn) {
	peer.owner.lock.Lock()
	defer peer.owner.lock.Unlock()
	peer.client = client
	go peer.serveClient(client)
}

func (peer *serverPeer) detachClient(client *clientConn) {
	peer.owner.lock.Lock()
	defer peer.owner.lock.Unlock()
	if peer.client == client {
		for _, remote := range peer.remotesByTarget {
			remote.removeLocked()
		}
		peer.client = nil
	}
}

func (peer *serverPeer) serveClient(client *clientConn) {
	defer client.Close()
	defer peer.detachClient(client)
	stream, err := client.ServiceChanges()
	if err != nil {
		if err != raw.ECONNCLOSED {
			log.Printf("peer changes stream: %s", err)
		}
		return
	}
	defer stream.Stop()
	for {
		changes, err := stream.Read()
		if err != nil {
			if err != io.EOF && err != raw.ECONNCLOSED {
				log.Printf("peer changes stream: %s", err)
			}
			break
		}
		if !peer.processChanges(client, changes) {
			break
		}
	}
}

func (peer *serverPeer) processChanges(client *clientConn, changes ServiceChanges) bool {
	peer.owner.lock.Lock()
	defer peer.owner.lock.Unlock()
	if peer.client != client || peer.failure != nil {
		// These changes are from the wrong client
		return false
	}
	for _, targetID := range changes.Removed {
		if remote := peer.remotesByTarget[targetID]; remote != nil {
			remote.removeLocked()
		}
	}
	for _, change := range changes.Changed {
		peer.addRemoteLocked(change)
		remote := peer.remotesByTarget[change.TargetID]
		if remote != nil && remote.name != change.Name {
			// target id changed to a new name, we treat as a removal
			remote.removeLocked()
			remote = nil
		}
	}
	return true
}

func (peer *serverPeer) addRemoteLocked(change ServiceChange) {
	if remote := peer.remotesByTarget[change.TargetID]; remote != nil {
		if remote.name == change.Name && change.Settings.Distance >= peer.distance {
			remote.settings = change.Settings
			// Notify subscriptions in case priority changed
			for sub := range remote.subscriptions {
				sub.updateRemoteLocked(remote)
			}
			return
		}
		// Either target id was reused for a different name (shouldn't happen
		// in practice), or we are not allowed to reach this remote anymore (its
		// maximum allowed distance is too small). We remove the old remote and
		// maybe readd it below.
		remote.removeLocked()
		remote = nil
	}

	if change.Settings.Distance < peer.distance {
		// We are not allowed to reach this service
		return
	}

	remote := &serverPeerRemote{
		peer:     peer,
		client:   peer.client,
		targetID: change.TargetID,
		name:     change.Name,
		settings: change.Settings,

		subscriptions: make(map[*serverSubscription]struct{}),
	}

	remotes := peer.remotesByName[remote.name]
	if remotes == nil {
		remotes = make(map[int64]*serverPeerRemote)
		peer.remotesByName[remote.name] = remotes
	}
	remotes[remote.targetID] = remote
	peer.remotesByTarget[remote.targetID] = remote

	for sub := range peer.owner.subsByName[remote.name] {
		sub.addRemoteLocked(remote)
	}
}

func (remote *serverPeerRemote) removeLocked() {
	peer := remote.peer
	if peer == nil || peer.client != remote.client {
		return
	}
	remote.peer = nil
	for sub := range remote.subscriptions {
		sub.removeRemoteLocked(remote)
	}
	if remotes := peer.remotesByName[remote.name]; remotes != nil {
		delete(remotes, remote.targetID)
		if len(remotes) == 0 {
			delete(peer.remotesByName, remote.name)
		}
	}
	delete(peer.remotesByTarget, remote.targetID)
}
