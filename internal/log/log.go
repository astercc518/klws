// Package log defines the structured logging interface used across wadist
// (formerly whatsmeow's waLog.Logger). The method set is unchanged so existing
// call sites are untouched; only the whatsmeow dependency is dropped.
package log

import "go.uber.org/zap"

// Logger is the structured logging interface used across wadist. Its method set
// matches the former waLog.Logger so call sites need no changes.
type Logger interface {
	Warnf(msg string, args ...any)
	Errorf(msg string, args ...any)
	Infof(msg string, args ...any)
	Debugf(msg string, args ...any)
	Sub(module string) Logger
}

// zapAdapter implements Logger on top of a zap SugaredLogger.
type zapAdapter struct{ s *zap.SugaredLogger }

var _ Logger = (*zapAdapter)(nil)

// New wraps an existing *zap.Logger as a Logger.
func New(base *zap.Logger) Logger { return &zapAdapter{s: base.Sugar()} }

// Production builds a JSON production logger and returns it as a Logger plus a
// flush func to defer at shutdown.
func Production() (Logger, func(), error) {
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

// Sub returns a child logger tagged with a module field, matching the former
// whatsmeow hierarchical logger convention.
func (z *zapAdapter) Sub(module string) Logger { return &zapAdapter{s: z.s.With("module", module)} }

// noop is a Logger that discards everything. Replaces the former waLog.Noop.
type noop struct{}

func (noop) Warnf(string, ...any)  {}
func (noop) Errorf(string, ...any) {}
func (noop) Infof(string, ...any)  {}
func (noop) Debugf(string, ...any) {}
func (noop) Sub(string) Logger     { return noop{} }

// Noop is a shared no-op Logger (drop-in for the former waLog.Noop), used by
// tests and by callers that pass nil.
var Noop Logger = noop{}
