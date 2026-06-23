// Package log adapts a zap logger to whatsmeow's waLog.Logger interface,
// so all subsystems (including whatsmeow internals) share one structured logger.
package log

import (
	waLog "go.mau.fi/whatsmeow/util/log"
	"go.uber.org/zap"
)

// zapAdapter implements waLog.Logger on top of a zap SugaredLogger.
type zapAdapter struct {
	s *zap.SugaredLogger
}

// Compile-time guarantee that the adapter satisfies whatsmeow's interface.
var _ waLog.Logger = (*zapAdapter)(nil)

// New wraps an existing *zap.Logger as a waLog.Logger.
func New(base *zap.Logger) waLog.Logger {
	return &zapAdapter{s: base.Sugar()}
}

// Production builds a JSON production logger and returns it as a waLog.Logger
// plus a flush func to defer at shutdown.
func Production() (waLog.Logger, func(), error) {
	l, err := zap.NewProduction()
	if err != nil {
		return nil, nil, err
	}
	return New(l), func() { _ = l.Sync() }, nil
}

func (z *zapAdapter) Warnf(msg string, args ...any)  { z.s.Warnf(msg, args...) }
func (z *zapAdapter) Errorf(msg string, args ...any) { z.s.Errorf(msg, args...) }
func (z *zapAdapter) Infof(msg string, args ...any)  { z.s.Infof(msg, args...) }
func (z *zapAdapter) Debugf(msg string, args ...any) { z.s.Debugf(msg, args...) }

// Sub returns a child logger tagged with a module field, matching whatsmeow's
// hierarchical logger convention.
func (z *zapAdapter) Sub(module string) waLog.Logger {
	return &zapAdapter{s: z.s.With("module", module)}
}
