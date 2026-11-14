package logbuf

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Handler 是一个输出 JSON 行并同时写入 Buffer 的 slog.Handler。
type Handler struct {
	out    io.Writer
	buf    *Buffer
	level  slog.Level
	attrs  []slog.Attr
	groups []string

	writeMu *sync.Mutex
}

// NewHandler 创建日志 Handler。out 通常是 os.Stdout。
func NewHandler(out io.Writer, buf *Buffer, level slog.Level) *Handler {
	return &Handler{
		out:     out,
		buf:     buf,
		level:   level,
		writeMu: &sync.Mutex{},
	}
}

// Enabled 判断日志级别是否启用。
func (h *Handler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

// Handle 格式化并写出一条日志。
func (h *Handler) Handle(_ context.Context, record slog.Record) error {
	entry := make(map[string]any, 8+len(h.attrs)+record.NumAttrs())

	for _, attr := range h.attrs {
		setAttr(entry, h.groups, attr)
	}

	record.Attrs(func(attr slog.Attr) bool {
		setAttr(entry, h.groups, attr)
		return true
	})

	entry["time"] = record.Time.Format(time.RFC3339Nano)
	entry["level"] = record.Level.String()
	entry["msg"] = record.Message

	line, err := json.Marshal(entry)
	if err != nil {
		fallback := fmt.Sprintf(`{"time":%q,"level":%q,"msg":%q,"log_error":%q}`,
			record.Time.Format(time.RFC3339Nano),
			record.Level.String(),
			record.Message,
			err.Error(),
		)
		line = []byte(fallback)
	}

	h.writeMu.Lock()
	_, _ = h.out.Write(append(line, '\n'))
	h.writeMu.Unlock()

	h.buf.Add(line)
	return nil
}

// WithAttrs 返回带附加属性的 Handler。
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clone := *h
	clone.attrs = append(append([]slog.Attr(nil), h.attrs...), attrs...)
	return &clone
}

// WithGroup 返回带属性分组的 Handler。
func (h *Handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}

	clone := *h
	clone.groups = append(append([]string(nil), h.groups...), name)
	return &clone
}

func setAttr(entry map[string]any, groups []string, attr slog.Attr) {
	attr.Value = attr.Value.Resolve()
	if attr.Equal(slog.Attr{}) {
		return
	}

	target := entry
	for _, group := range groups {
		next, ok := target[group].(map[string]any)
		if !ok {
			next = make(map[string]any)
			target[group] = next
		}
		target = next
	}

	if attr.Value.Kind() == slog.KindGroup {
		groupMap, ok := target[attr.Key].(map[string]any)
		if !ok {
			groupMap = make(map[string]any)
			target[attr.Key] = groupMap
		}

		for _, item := range attr.Value.Group() {
			setAttr(groupMap, nil, item)
		}
		return
	}

	target[attr.Key] = valueOf(attr.Value)
}

func valueOf(value slog.Value) any {
	switch value.Kind() {
	case slog.KindString:
		return value.String()
	case slog.KindInt64:
		return value.Int64()
	case slog.KindUint64:
		return value.Uint64()
	case slog.KindFloat64:
		return value.Float64()
	case slog.KindBool:
		return value.Bool()
	case slog.KindTime:
		return value.Time().Format(time.RFC3339Nano)
	case slog.KindDuration:
		return value.Duration().String()
	case slog.KindAny:
		if err, ok := value.Any().(error); ok {
			return err.Error()
		}
		return value.Any()
	default:
		return value.String()
	}
}

// ParseLevel 把 debug/info/warn/error 转成 slog.Level。
func ParseLevel(text string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
