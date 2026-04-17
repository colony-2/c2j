package input

import (
	"sync"

	"github.com/colony-2/c2j/pkg/ops"
)

// SimpleSSEManager is a basic implementation of SSEManager
type SimpleSSEManager struct {
	mu          sync.RWMutex
	clients     map[string]chan ops.SSEEvent
	eventBuffer []ops.SSEEvent
	bufferSize  int
}

// NewSimpleSSEManager creates a new SSE manager
func NewSimpleSSEManager() *SimpleSSEManager {
	return &SimpleSSEManager{
		clients:     make(map[string]chan ops.SSEEvent),
		eventBuffer: make([]ops.SSEEvent, 0, 100),
		bufferSize:  100,
	}
}

// Subscribe adds a client and returns their event channel
func (m *SimpleSSEManager) Subscribe(clientID string) <-chan ops.SSEEvent {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Create a buffered channel for the client
	ch := make(chan ops.SSEEvent, 10)
	m.clients[clientID] = ch

	// Send recent events from buffer
	for _, event := range m.eventBuffer {
		select {
		case ch <- event:
		default:
			// Skip if channel is full
		}
	}

	return ch
}

// Unsubscribe removes a client
func (m *SimpleSSEManager) Unsubscribe(clientID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if ch, exists := m.clients[clientID]; exists {
		close(ch)
		delete(m.clients, clientID)
	}
}

// Broadcast sends an event to all connected clients
func (m *SimpleSSEManager) Broadcast(event ops.SSEEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Add to buffer
	m.eventBuffer = append(m.eventBuffer, event)
	if len(m.eventBuffer) > m.bufferSize {
		// Remove oldest events if buffer is full
		m.eventBuffer = m.eventBuffer[len(m.eventBuffer)-m.bufferSize:]
	}

	// Send to all clients
	for clientID, ch := range m.clients {
		select {
		case ch <- event:
		default:
			// Client channel is full, skip
			// In production, you might want to close slow clients
			_ = clientID // Avoid unused variable warning
		}
	}
}

// GetClients returns the number of connected clients
func (m *SimpleSSEManager) GetClients() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.clients)
}

// ClearBuffer clears the event buffer
func (m *SimpleSSEManager) ClearBuffer() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.eventBuffer = m.eventBuffer[:0]
}
