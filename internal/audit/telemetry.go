package audit

import (
	"log/slog"
	"sync/atomic"
	"time"
)

type auditTelemetry struct {
	persistenceFailures    atomic.Int64
	lastPersistenceFailure atomic.Int64
}

func (s *Server) notePersistenceFailure(id, stage string) {
	s.telemetry.persistenceFailures.Add(1)
	s.telemetry.lastPersistenceFailure.Store(time.Now().Unix())
	slog.Error("audit persistence failed", "request_id", id, "stage", stage)
}
