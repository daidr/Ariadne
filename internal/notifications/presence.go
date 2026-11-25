package notifications

import (
	"sync"
	"time"
)

const (
	// DefaultHeartbeatTTL expires focused clients that disappeared without sending blur.
	DefaultHeartbeatTTL = 45 * time.Second
	// DefaultLeaveGrace suppresses pushes briefly after blur/hide to avoid noisy flaps
	// while ensuring offline pushes still arrive quickly once the user steps away.
	DefaultLeaveGrace  = 10 * time.Second
	maxPresenceClients = 128
)

type presenceEntry struct {
	focused   bool
	projectID string
	threadID  string
	updatedAt time.Time
	blurredAt time.Time
}

// PresenceTracker tracks live foreground browser tabs in memory so the server can
// suppress Web Push before sending—preventing iOS/macOS WebKit from revoking the
// subscription after three silent pushes.
type PresenceTracker struct {
	mu           sync.Mutex
	now          func() time.Time
	heartbeatTTL time.Duration
	leaveGrace   time.Duration
	clients      map[string]presenceEntry
}

func NewPresenceTracker(now func() time.Time) *PresenceTracker {
	if now == nil {
		now = time.Now
	}
	return &PresenceTracker{
		now:          now,
		heartbeatTTL: DefaultHeartbeatTTL,
		leaveGrace:   DefaultLeaveGrace,
		clients:      map[string]presenceEntry{},
	}
}

func (tracker *PresenceTracker) Report(clientID string, focused bool, projectID, threadID string) {
	if clientID == "" {
		return
	}
	now := tracker.now()
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	tracker.pruneLocked(now)
	prev, exists := tracker.clients[clientID]
	entry := presenceEntry{
		focused:   focused,
		projectID: projectID,
		threadID:  threadID,
		updatedAt: now,
	}
	if !focused {
		if exists && prev.focused {
			entry.blurredAt = now
		} else if exists && !prev.blurredAt.IsZero() {
			entry.blurredAt = prev.blurredAt
		} else {
			// A client reporting unfocused without prior focus does not create a grace window.
			delete(tracker.clients, clientID)
			return
		}
	}
	if !exists && len(tracker.clients) >= maxPresenceClients {
		var oldestID string
		var oldestTime time.Time
		for id, item := range tracker.clients {
			if oldestID == "" || item.updatedAt.Before(oldestTime) {
				oldestID = id
				oldestTime = item.updatedAt
			}
		}
		if oldestID != "" {
			delete(tracker.clients, oldestID)
		}
	}
	tracker.clients[clientID] = entry
}

// IsActive returns true when at least one client is actively focused (within TTL)
// or within the brief post-blur leave grace window.
func (tracker *PresenceTracker) IsActive() bool {
	if tracker == nil {
		return false
	}
	now := tracker.now()
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	tracker.pruneLocked(now)
	for _, entry := range tracker.clients {
		if entry.focused && now.Sub(entry.updatedAt) <= tracker.heartbeatTTL {
			return true
		}
		if !entry.focused && !entry.blurredAt.IsZero() && now.Sub(entry.blurredAt) <= tracker.leaveGrace {
			return true
		}
	}
	return false
}

func (tracker *PresenceTracker) pruneLocked(now time.Time) {
	for id, entry := range tracker.clients {
		if entry.focused {
			if now.Sub(entry.updatedAt) > tracker.heartbeatTTL {
				delete(tracker.clients, id)
			}
			continue
		}
		if entry.blurredAt.IsZero() || now.Sub(entry.blurredAt) > tracker.leaveGrace {
			delete(tracker.clients, id)
		}
	}
}
