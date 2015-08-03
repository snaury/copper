package copper

import (
	"fmt"
	"io"
	"net"
	"time"
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

func (remote *serverPeerRemote) handleRequestLocked(callback handleRequestCallback) handleRequestStatus {
	if peer := remote.peer; peer != nil {
		peer.owner.lock.Unlock()
		defer peer.owner.lock.Lock()
		stream, err := rpcNewStream(remote.client, remote.targetID)
		if err != nil {
			return handleRequestStatusImpossible
		}
		defer stream.Close()
		return callback(stream)
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
			if log := DebugLog(); log != nil {
				log.Printf("Peer %s: connection failed: %s", peer.key.address, err)
			}
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
		<-client.Done()
		peer.detachClient(client)
		client.Close()
		close(stop)
	}
}

func (peer *serverPeer) attachClient(client *clientConn) {
	peer.owner.lock.Lock()
	defer peer.owner.lock.Unlock()
	peer.client = client
	if log := DebugLog(); log != nil {
		log.Printf("Peer %s: connected to %s", peer.key.address, client.RemoteAddr())
	}
	go peer.serveClient(client)
}

func (peer *serverPeer) detachClient(client *clientConn) {
	peer.owner.lock.Lock()
	defer peer.owner.lock.Unlock()
	if log := DebugLog(); log != nil {
		log.Printf("Peer %s: disconnected from %s", peer.key.address, client.RemoteAddr())
	}
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
	for {
		if !peer.listenChanges(client) {
			break
		}
		peer.owner.lock.Lock()
		active := peer.client == client && peer.failure == nil
		peer.owner.lock.Unlock()
		if !active {
			break
		}
	}
}

func (peer *serverPeer) listenChanges(client *clientConn) bool {
	stream, err := client.ServiceChanges()
	if err != nil {
		if err != ECONNCLOSED && err != ECONNSHUTDOWN {
			if log := ErrorLog(); log != nil {
				log.Printf("Peer %s: changes stream: %s: %s", peer.key.address, client.RemoteAddr(), err)
			}
		} else {
			if log := DebugLog(); log != nil {
				log.Printf("Peer %s: changes stream: %s: %s", peer.key.address, client.RemoteAddr(), err)
			}
		}
		return false
	}
	defer stream.Stop()
	for {
		changes, err := stream.Read()
		if err != nil {
			if err == ECONNCLOSED || err == ECONNSHUTDOWN {
				if log := DebugLog(); log != nil {
					log.Printf("Peer %s: changes stream: %s: %s", peer.key.address, client.RemoteAddr(), err)
				}
				return false
			}
			if err != io.EOF {
				if log := ErrorLog(); log != nil {
					log.Printf("Peer %s: changes stream: %s: %s", peer.key.address, client.RemoteAddr(), err)
				}
			}
			// TODO: Theoretically we may receive an error if we are not reading
			// changes fast enough, in which case we may try to reconnect, but
			// for that we need to forget all currently active services first.
			if log := DebugLog(); log != nil {
				log.Printf("Peer %s: changes stream: %s: %s", peer.key.address, client.RemoteAddr(), err)
			}
			return false
		}
		if !peer.processChanges(client, changes) {
			if log := DebugLog(); log != nil {
				log.Printf("Peer %s: changes stream: %s: stopping", peer.key.address, client.RemoteAddr())
			}
			return false
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
			if log := DebugLog(); log != nil {
				log.Printf("Service %s(priority=%d): removed from %s", remote.name, remote.settings.Priority, peer.key.address)
			}
		}
	}
	for _, change := range changes.Changed {
		peer.addRemoteLocked(change)
	}
	return true
}

func (peer *serverPeer) addRemoteLocked(change ServiceChange) {
	if change.Settings.Distance < peer.distance {
		// We are not allowed to reach this service
		if remote := peer.remotesByTarget[change.TargetID]; remote != nil {
			// Forget this remote and remove from all subscriptions
			remote.removeLocked()
			if log := DebugLog(); log != nil {
				log.Printf("Service %s(priority=%d): removed from %s", remote.name, remote.settings.Priority, peer.key.address)
			}
		}
		return
	}

	// Change distance to the peer distance, not the maximum allowed distance
	change.Settings.Distance = peer.distance

	// Check if we already had this remote registered
	if remote := peer.remotesByTarget[change.TargetID]; remote != nil {
		if remote.name == change.Name && change.Settings.Priority == remote.settings.Priority {
			// This remote changed neigher name nor priority
			remote.settings = change.Settings
			return
		}
		// Either target id was reused for a different name (shouldn't happen
		// in practice), or priority changed. The easiest way to update
		// subscriptions is to simply remove this remote and re-add it again
		// below.
		remote.removeLocked()
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

	if log := DebugLog(); log != nil {
		log.Printf("Service %s(priority=%d): discovered at %s", remote.name, remote.settings.Priority, peer.key.address)
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
