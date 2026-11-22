package nl2sql

import (
	"context"
	"errors"
	"sync"
	"time"
)

const historyLimit = 5
const historyTTL = 30 * time.Minute
const sessionLimit = 512

var ErrTooManySessions = errors.New("session capacity reached")

type Exchange struct {
	Question string
	Answer   string
}

type session struct {
	turns     []Exchange
	lease     chan struct{}
	users     int
	idleSince time.Time
}

type History struct {
	mu       sync.Mutex
	sessions map[string]*session
}

func NewHistory() *History {
	return &History{sessions: make(map[string]*session)}
}

// Run serializes requests within a session, and commits only a completed turn.
func (h *History) Run(ctx context.Context, id string, ask func([]Exchange) (Answer, error), question string) (Answer, error) {
	if id == "" {
		return ask(nil)
	}
	s, err := h.acquire(ctx, id)
	if err != nil {
		return Answer{}, err
	}
	defer func() {
		<-s.lease
		h.release(s)
	}()
	answer, err := ask(s.turns)
	if err == nil && answer.Completed && ctx.Err() == nil {
		if len(s.turns) == historyLimit {
			copy(s.turns, s.turns[1:])
			s.turns = s.turns[:historyLimit-1]
		}
		s.turns = append(s.turns, Exchange{Question: capOutput(question), Answer: capOutput(answer.Text)})
	}
	return answer, err
}

func (h *History) acquire(ctx context.Context, id string) (*session, error) {
	h.mu.Lock()
	now := time.Now()
	for key, s := range h.sessions {
		if s.users == 0 && now.Sub(s.idleSince) >= historyTTL {
			delete(h.sessions, key)
		}
	}
	s := h.sessions[id]
	if s == nil {
		if len(h.sessions) >= sessionLimit {
			h.mu.Unlock()
			return nil, ErrTooManySessions
		}
		s = &session{lease: make(chan struct{}, 1)}
		h.sessions[id] = s
	}
	s.users++
	h.mu.Unlock()
	select {
	case s.lease <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-s.lease
			h.release(s)
			return nil, err
		}
		return s, nil
	case <-ctx.Done():
		h.release(s)
		return nil, ctx.Err()
	}
}

func (h *History) release(s *session) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s.users--
	if s.users == 0 {
		s.idleSince = time.Now()
	}
}
