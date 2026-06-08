package main

import (
	"log/slog"

	"github.com/albertocavalcante/bzlhub/internal/backend"
	"github.com/albertocavalcante/bzlhub/internal/bzlhub"
)

// attachMirror wires the Mirror onto cs when bk is a BCRMirror, and
// logs LastSyncReadErr at WARN so a corrupt LAST_SYNC surfaces
// consistently across every code path that opens a Mirror — not
// just `bzlhub serve`. Callers in cmd/bzlhub/* should use this
// rather than reaching into bk themselves.
func attachMirror(cs *bzlhub.Service, bk backend.Backend, log *slog.Logger) {
	mb, ok := bk.(*backend.BCRMirror)
	if !ok {
		return
	}
	m := mb.Mirror()
	cs.UseMirror(m)
	if syncErr := m.LastSyncReadErr(); syncErr != nil {
		log.Warn("LAST_SYNC unreadable, seeded to zero", "err", syncErr)
	}
}
