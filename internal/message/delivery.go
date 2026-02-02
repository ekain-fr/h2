package message

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
)

// IdleFunc returns true if the child process is considered idle.
type IdleFunc func() bool

// DeliveryConfig holds configuration for the delivery goroutine.
type DeliveryConfig struct {
	Queue     *MessageQueue
	AgentName string
	PtyWriter io.Writer  // writes to the child PTY
	IsIdle    IdleFunc   // checks if child is idle
	OnDeliver func()     // called after each delivery (e.g. to render)
	Stop      <-chan struct{}
}

// PrepareMessage creates a Message, writes its body to disk, and enqueues it.
// Returns the message ID.
func PrepareMessage(q *MessageQueue, agentName, from, body string, priority Priority) (string, error) {
	id := uuid.New().String()
	now := time.Now()

	dir := filepath.Join(os.Getenv("HOME"), ".h2", "messages", agentName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create message dir: %w", err)
	}

	filename := fmt.Sprintf("%s-%s.md", now.Format("20060102-150405"), id[:8])
	filePath := filepath.Join(dir, filename)
	if err := os.WriteFile(filePath, []byte(body), 0o600); err != nil {
		return "", fmt.Errorf("write message file: %w", err)
	}

	msg := &Message{
		ID:        id,
		From:      from,
		Priority:  priority,
		Body:      body,
		FilePath:  filePath,
		Status:    StatusQueued,
		CreatedAt: now,
	}
	q.Enqueue(msg)
	return id, nil
}

// RunDelivery runs the delivery loop that drains the queue and writes to the PTY.
// It blocks until cfg.Stop is closed.
func RunDelivery(cfg DeliveryConfig) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-cfg.Stop:
			return
		case <-cfg.Queue.Notify():
		case <-ticker.C:
		}

		for {
			idle := cfg.IsIdle != nil && cfg.IsIdle()
			msg := cfg.Queue.Dequeue(idle)
			if msg == nil {
				break
			}
			deliver(cfg, msg)
		}
	}
}

func deliver(cfg DeliveryConfig, msg *Message) {
	if msg.Priority == PriorityInterrupt {
		// Send ctrl+c to interrupt the child.
		cfg.PtyWriter.Write([]byte{0x03})
		time.Sleep(200 * time.Millisecond)
	}

	line := fmt.Sprintf("[h2-message from=%s id=%s priority=%s] Read %s\r",
		msg.From, msg.ID, msg.Priority, msg.FilePath)
	cfg.PtyWriter.Write([]byte(line))

	now := time.Now()
	msg.Status = StatusDelivered
	msg.DeliveredAt = &now

	if cfg.OnDeliver != nil {
		cfg.OnDeliver()
	}
}
