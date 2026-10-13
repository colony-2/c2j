package secrets

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"log/slog"
	"reflect"
	"sync"
)

var activeMasks sync.Map // *Redactor -> struct{}; no material outside execution redactors
var logMu sync.Mutex

func registerMask(s *Redactor) {
	activeMasks.Store(s, struct{}{})
	logMu.Lock()
	defer logMu.Unlock()
	// The standard slog handler writes through log.Default. SetDefault with a
	// wrapper around it would bridge log back into slog and deadlock. Mask its
	// writer instead; this also covers loggers captured before the session.
	t := reflect.TypeOf(slog.Default().Handler())
	if t.Kind() == reflect.Pointer && t.Elem().PkgPath() == "log/slog" && t.Elem().Name() == "defaultHandler" {
		if _, ok := log.Writer().(*maskWriter); !ok {
			log.SetOutput(&maskWriter{log.Writer()})
		}
		return
	}
	if _, ok := slog.Default().Handler().(*maskHandler); !ok {
		slog.SetDefault(slog.New(&maskHandler{base: slog.Default().Handler()}))
	}
}

type maskWriter struct{ base io.Writer }

func (w *maskWriter) Write(p []byte) (int, error) {
	_, err := io.WriteString(w.base, redactLog(string(p)))
	if err != nil {
		return 0, err
	}
	return len(p), nil
}
func redactLog(s string) string {
	activeMasks.Range(func(k, v any) bool { s = k.(*Redactor).Redact(s); return true })
	return s
}

type maskHandler struct {
	base   slog.Handler
	attrs  []slog.Attr
	groups []string
}

func (h *maskHandler) Enabled(ctx context.Context, l slog.Level) bool { return h.base.Enabled(ctx, l) }
func maskAttr(a slog.Attr) slog.Attr {
	a.Key = redactLog(a.Key)
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindGroup:
		attrs := v.Group()
		out := make([]slog.Attr, len(attrs))
		for i, a := range attrs {
			out[i] = maskAttr(a)
		}
		a.Value = slog.GroupValue(out...)
	case slog.KindString:
		a.Value = slog.StringValue(redactLog(v.String()))
	case slog.KindAny:
		if err, ok := v.Any().(error); ok {
			a.Value = slog.StringValue(redactLog(err.Error()))
		} else if value, ok := v.Any().(fmt.Stringer); ok {
			a.Value = slog.StringValue(redactLog(value.String()))
		} else if b, err := json.Marshal(v.Any()); err == nil {
			safe := redactLog(string(b))
			if safe != string(b) {
				a.Value = slog.StringValue(safe)
			}
		} else {
			a.Value = slog.StringValue(redactLog(fmt.Sprint(v.Any())))
		}
	}
	return a
}
func (h *maskHandler) Handle(ctx context.Context, r slog.Record) error {
	out := slog.NewRecord(r.Time, r.Level, redactLog(r.Message), r.PC)
	attrs := make([]slog.Attr, 0, r.NumAttrs())
	for _, a := range h.attrs {
		out.AddAttrs(maskAttr(a))
	}
	r.Attrs(func(a slog.Attr) bool { attrs = append(attrs, maskAttr(a)); return true })
	for i := len(h.groups) - 1; i >= 0; i-- {
		attrs = []slog.Attr{slog.Attr{Key: redactLog(h.groups[i]), Value: slog.GroupValue(attrs...)}}
	}
	out.AddAttrs(attrs...)
	return h.base.Handle(ctx, out)
}
func (h *maskHandler) WithAttrs(a []slog.Attr) slog.Handler {
	c := *h
	attrs := make([]slog.Attr, len(a))
	for i, v := range a {
		attrs[i] = maskAttr(v)
	}
	for i := len(h.groups) - 1; i >= 0; i-- {
		attrs = []slog.Attr{{Key: redactLog(h.groups[i]), Value: slog.GroupValue(attrs...)}}
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
