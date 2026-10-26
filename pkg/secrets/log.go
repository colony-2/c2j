package secrets

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
)

// MaskLogger binds masking to one task's redactor. It never changes global
// loggers. Bind before With/WithGroup so later-resolved values are also masked.
func MaskLogger(logger *slog.Logger, redactor *Redactor) *slog.Logger {
	if logger == nil {
		logger = slog.Default()
	}
	if redactor == nil {
		return logger
	}
	if h, ok := logger.Handler().(*maskHandler); ok && h.redactor == redactor {
		return logger
	}
	return slog.New(&maskHandler{base: logger.Handler(), redactor: redactor})
}

type maskHandler struct {
	redactor *Redactor
	base     slog.Handler
	attrs    []slog.Attr
	groups   []string
}

func (h *maskHandler) Enabled(ctx context.Context, l slog.Level) bool { return h.base.Enabled(ctx, l) }
func (h *maskHandler) maskAttr(a slog.Attr) slog.Attr {
	a.Key = h.redactor.Redact(a.Key)
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindGroup:
		attrs := v.Group()
		out := make([]slog.Attr, len(attrs))
		for i, a := range attrs {
			out[i] = h.maskAttr(a)
		}
		a.Value = slog.GroupValue(out...)
	case slog.KindString:
		a.Value = slog.StringValue(h.redactor.Redact(v.String()))
	case slog.KindAny:
		if err, ok := v.Any().(error); ok {
			a.Value = slog.StringValue(h.redactor.Redact(err.Error()))
		} else if value, ok := v.Any().(fmt.Stringer); ok {
			a.Value = slog.StringValue(h.redactor.Redact(value.String()))
		} else if b, err := json.Marshal(v.Any()); err == nil {
			safe := h.redactor.Redact(string(b))
			if safe != string(b) {
				a.Value = slog.StringValue(safe)
			}
		} else {
			a.Value = slog.StringValue(h.redactor.Redact(fmt.Sprint(v.Any())))
		}
	}
	return a
}
func (h *maskHandler) Handle(ctx context.Context, r slog.Record) error {
	out := slog.NewRecord(r.Time, r.Level, h.redactor.Redact(r.Message), r.PC)
	attrs := make([]slog.Attr, 0, r.NumAttrs())
	for _, a := range h.attrs {
		out.AddAttrs(h.maskAttr(a))
	}
	r.Attrs(func(a slog.Attr) bool { attrs = append(attrs, h.maskAttr(a)); return true })
	for i := len(h.groups) - 1; i >= 0; i-- {
		attrs = []slog.Attr{slog.Attr{Key: h.redactor.Redact(h.groups[i]), Value: slog.GroupValue(attrs...)}}
	}
	out.AddAttrs(attrs...)
	return h.base.Handle(ctx, out)
}
func (h *maskHandler) WithAttrs(a []slog.Attr) slog.Handler {
	c := *h
	attrs := make([]slog.Attr, len(a))
	for i, v := range a {
		attrs[i] = h.maskAttr(v)
	}
	for i := len(h.groups) - 1; i >= 0; i-- {
		attrs = []slog.Attr{{Key: h.redactor.Redact(h.groups[i]), Value: slog.GroupValue(attrs...)}}
	}
	c.attrs = append(append([]slog.Attr(nil), h.attrs...), attrs...)
	return &c
}
func (h *maskHandler) WithGroup(g string) slog.Handler {
	if g == "" {
		return h
	}
	c := *h
	c.groups = append(append([]string(nil), h.groups...), g)
	return &c
}
