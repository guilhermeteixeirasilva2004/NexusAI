package fakes

import (
	"testing"

	"nexusai/internal/core/ports"
	"nexusai/internal/core/ports/contract"
)

func TestSessionsContract(t *testing.T) {
	contract.SessionStore(t, func(*testing.T) ports.SessionStore { return NewSessions() })
}

func TestMemoryContract(t *testing.T) {
	contract.MemoryStore(t, func(*testing.T) ports.MemoryStore { return &Memory{} })
}
