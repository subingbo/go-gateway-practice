package config

import (
	"context"
	"log/slog"
	"os"
	"time"
)

// Watcher 轮询配置文件的变化并触发重载。
//
// 没有引入 fsnotify，是因为轮询 mtime + size 在 Windows 和 Linux 上
// 行为一致，足够演示“配置热更新”。
type Watcher struct {
	path   string
	logger *slog.Logger

	lastMod  time.Time
	lastSize int64
}

// NewWatcher 创建配置文件监听器。
func NewWatcher(path string, logger *slog.Logger) *Watcher {
	watcher := &Watcher{path: path, logger: logger}

	if info, err := os.Stat(path); err == nil {
		watcher.lastMod = info.ModTime()
		watcher.lastSize = info.Size()
	}

	return watcher
}

// Run 每 2 秒检查一次配置；变化且校验通过时调用 onChange。
func (w *Watcher) Run(ctx context.Context, onChange func(*Config)) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.poll(onChange)
		}
	}
}

func (w *Watcher) poll(onChange func(*Config)) {
	info, err := os.Stat(w.path)
	if err != nil {
		w.logger.Warn("config watcher stat failed", "path", w.path, "error", err)
		return
	}

	if info.ModTime().Equal(w.lastMod) && info.Size() == w.lastSize {
		return
	}

	w.lastMod = info.ModTime()
	w.lastSize = info.Size()

	cfg, err := Load(w.path)
	if err != nil {
		w.logger.Error("config reload rejected; keeping previous config", "error", err)
		return
	}

	w.logger.Info("config reloaded", "path", w.path, "routes", len(cfg.Routes))
	onChange(cfg)
}
