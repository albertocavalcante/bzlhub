package canopy

import (
	"context"
	"time"

	"github.com/albertocavalcante/canopy/internal/api"
	"github.com/albertocavalcante/canopy/internal/store"
)

func (s *Service) History(ctx context.Context, opts api.HistoryOptions) ([]api.AuditEvent, error) {
	rows, err := s.store.ListAudit(ctx, store.AuditQuery{
		Kinds:  opts.Kinds,
		Source: opts.Source,
		Module: opts.Module,
		Limit:  opts.Limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]api.AuditEvent, len(rows))
	for i, r := range rows {
		out[i] = api.AuditEvent{
			ID:         r.ID,
			Timestamp:  r.Timestamp.UTC().Format(time.RFC3339Nano),
			Kind:       r.Kind,
			Source:     r.Source,
			Module:     r.Module,
			Version:    r.Version,
			OK:         r.OK,
			DurationMs: r.DurationMs,
			Error:      r.Error,
			Payload:    r.Payload,
		}
	}
	return out, nil
}
