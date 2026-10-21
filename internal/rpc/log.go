package rpc

import (
	"context"
	"log/slog"

	"github.com/iantanwx/loop/internal/secrets"
)

// RedactingHandler passes every log record through the redactor before the
// next handler writes it (KTD27): the message and every string value,
// including values inside groups.
type RedactingHandler struct {
	Next     slog.Handler
	Redactor *secrets.Redactor
}

// NewRedactingLogger returns a logger whose every line is redacted.
func NewRedactingLogger(next slog.Handler, red *secrets.Redactor) *slog.Logger {
	return slog.New(&RedactingHandler{Next: next, Redactor: red})
}

func (h *RedactingHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.Next.Enabled(ctx, l)
}

func (h *RedactingHandler) Handle(ctx context.Context, r slog.Record) error {
	out := slog.NewRecord(r.Time, r.Level, h.Redactor.Redact(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(h.attr(a))
		return true
	})
	return h.Next.Handle(ctx, out)
}

func (h *RedactingHandler) attr(a slog.Attr) slog.Attr {
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindString:
		return slog.String(a.Key, h.Redactor.Redact(v.String()))
	case slog.KindGroup:
		attrs := v.Group()
		out := make([]any, 0, len(attrs))
		for _, g := range attrs {
			out = append(out, h.attr(g))
		}
		return slog.Group(a.Key, out...)
	case slog.KindAny:
		if err, ok := v.Any().(error); ok {
			return slog.String(a.Key, h.Redactor.Redact(err.Error()))
		}
		return slog.String(a.Key, h.Redactor.Redact(v.String()))
	}
	return slog.Attr{Key: a.Key, Value: v}
}

func (h *RedactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	red := make([]slog.Attr, 0, len(attrs))
	for _, a := range attrs {
		red = append(red, h.attr(a))
	}
	return &RedactingHandler{Next: h.Next.WithAttrs(red), Redactor: h.Redactor}
}

func (h *RedactingHandler) WithGroup(name string) slog.Handler {
	return &RedactingHandler{Next: h.Next.WithGroup(name), Redactor: h.Redactor}
}
