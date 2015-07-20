package copper

import (
	"fmt"
	"time"
)

// rawConnSettings is used to keep track of settings
// It uses the connection lock for protecting its fields
type rawConnSettings struct {
	conn      *rawConn
	callbacks []func(error)

	localConnWindowSize     int
	remoteConnWindowSize    int
	localStreamWindowSize   int
	remoteStreamWindowSize  int
	localInactivityTimeout  time.Duration
	remoteInactivityTimeout time.Duration
}

func (s *rawConnSettings) init(conn *rawConn) {
	s.conn = conn
	s.localConnWindowSize = InitialConnectionWindow
	s.remoteConnWindowSize = InitialConnectionWindow
	s.localStreamWindowSize = InitialStreamWindow
	s.remoteStreamWindowSize = InitialStreamWindow
	s.localInactivityTimeout = InitialInactivityTimeout
	s.remoteInactivityTimeout = InitialInactivityTimeout
}

// Fails all pending callbacks with err, called when closing the connection
func (s *rawConnSettings) failLocked(err error) {
	callbacks := s.callbacks
	s.callbacks = nil
	for _, callback := range callbacks {
		if callback != nil {
			callback(err)
		}
	}
}

// Handles an incoming settings ack
// Find and call the callback that registered for the outgoing frame.
func (s *rawConnSettings) handleAck() {
	var callback func(error)
	s.conn.mu.Lock()
	if len(s.callbacks) > 0 {
		callback = s.callbacks[0]
		copy(s.callbacks, s.callbacks[1:])
		s.callbacks[len(s.callbacks)-1] = nil
		s.callbacks = s.callbacks[:len(s.callbacks)-1]
	}
	s.conn.mu.Unlock()
	if callback != nil {
		callback(nil)
	}
}

// Handles an incoming settings frame, updates to new settings
func (s *rawConnSettings) handleSettings(frame *SettingsFrame) error {
	s.conn.mu.Lock()
	if value, ok := frame.Value(SettingConnWindow); ok {
		if value < MinWindowSize || value > MaxWindowSize {
			s.conn.mu.Unlock()
			return copperError{
				error: fmt.Errorf("cannot set connection window to %d bytes", value),
				code:  EINVALIDFRAME,
			}
		}
		diff := int(value) - s.remoteConnWindowSize
		s.conn.outgoing.changeWriteWindow(diff)
		s.remoteConnWindowSize = int(value)
	}
	if value, ok := frame.Value(SettingStreamWindow); ok {
		if value < MinWindowSize || value > MaxWindowSize {
			s.conn.mu.Unlock()
			return copperError{
				error: fmt.Errorf("cannot set stream window to %d bytes", value),
				code:  EINVALIDFRAME,
			}
		}
		diff := int(value) - s.remoteStreamWindowSize
		s.conn.streams.changeWriteWindow(diff)
		s.remoteStreamWindowSize = int(value)
	}
	if value, ok := frame.Value(SettingInactivityMilliseconds); ok {
		if value < 1000 {
			s.conn.mu.Unlock()
			return copperError{
				error: fmt.Errorf("cannot set inactivity timeout to %dms", value),
				code:  EINVALIDFRAME,
			}
		}
		s.remoteInactivityTimeout = time.Duration(value) * time.Millisecond
	}
	s.conn.outgoing.addSettingsAck()
	s.conn.mu.Unlock()
	return nil
}

func (s *rawConnSettings) getLocalStreamWindowSize() int {
	s.conn.mu.RLock()
	window := s.localStreamWindowSize
	s.conn.mu.RUnlock()
	return window
}

func (s *rawConnSettings) getRemoteStreamWindowSize() int {
	s.conn.mu.RLock()
	window := s.remoteStreamWindowSize
	s.conn.mu.RUnlock()
	return window
}

func (s *rawConnSettings) getLocalInactivityTimeout() time.Duration {
	s.conn.mu.RLock()
	d := s.localInactivityTimeout
	s.conn.mu.RUnlock()
	return d
}

func (s *rawConnSettings) getRemoteInactivityTimeout() time.Duration {
	s.conn.mu.RLock()
	d := s.remoteInactivityTimeout
	s.conn.mu.RUnlock()
	return d
}
