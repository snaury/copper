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
	s.localConnWindowSize = defaultConnWindowSize
	s.remoteConnWindowSize = defaultConnWindowSize
	s.localStreamWindowSize = defaultStreamWindowSize
	s.remoteStreamWindowSize = defaultStreamWindowSize
	s.localInactivityTimeout = defaultInactivityTimeout
	s.remoteInactivityTimeout = defaultInactivityTimeout
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
func (s *rawConnSettings) handleSettings(frame *settingsFrame) error {
	s.conn.mu.Lock()
	for key, value := range frame.values {
		switch key {
		case settingConnWindow:
			if value < minWindowSize || value > maxWindowSize {
				s.conn.mu.Unlock()
				return copperError{
					error: fmt.Errorf("cannot set connection window to %d bytes", value),
					code:  EINVALIDFRAME,
				}
			}
			diff := int(value) - s.remoteConnWindowSize
			s.conn.outgoing.changeWriteWindow(diff)
			s.remoteConnWindowSize = int(value)
		case settingStreamWindow:
			if value < minWindowSize || value > maxWindowSize {
				s.conn.mu.Unlock()
				return copperError{
					error: fmt.Errorf("cannot set stream window to %d bytes", value),
					code:  EINVALIDFRAME,
				}
			}
			diff := int(value) - s.remoteStreamWindowSize
			s.conn.streams.changeWriteWindow(diff)
			s.remoteStreamWindowSize = int(value)
		case settingInactivityMilliseconds:
			if value < 1000 {
				s.conn.mu.Unlock()
				return copperError{
					error: fmt.Errorf("cannot set inactivity timeout to %dms", value),
					code:  EINVALIDFRAME,
				}
			}
			s.remoteInactivityTimeout = time.Duration(value) * time.Millisecond
		default:
			s.conn.mu.Unlock()
			return copperError{
				error: fmt.Errorf("unknown settings key %d", key),
				code:  EINVALIDFRAME,
			}
		}
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
