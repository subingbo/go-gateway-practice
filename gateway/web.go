package main

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed all:web/dist
var webDist embed.FS

// registerUI 托管测试看板的静态资源
func registerUI(mux *http.ServeMux) {
	dist, err := fs.Sub(webDist, "web/dist")
	if err != nil {
		mux.HandleFunc("/", webNotBuiltHandler)
		return
	}

	index, err := fs.Stat(dist, "index.html")
	if err != nil || index.IsDir() {
		mux.HandleFunc("/", webNotBuiltHandler)
		return
	}

	mux.Handle("/", http.FileServerFS(dist))
}

func webNotBuiltHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusServiceUnavailable)
	w.Write([]byte(webNotBuiltHTML))
}

const webNotBuiltHTML = `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <title>Gateway Console</title>
  <style>
    body {
      margin: 0;
      min-height: 100vh;
      display: grid;
      place-items: center;
      background: #faf2e7;
      color: #35261e;
      font-family: "Segoe UI", "PingFang SC", "Microsoft YaHei", sans-serif;
    }
    main {
      max-width: 560px;
      padding: 48px;
      border-radius: 24px;
      background: #fff8ef;
      box-shadow: 0 24px 60px -32px rgba(120, 63, 38, 0.45);
    }
    h1 { margin: 0 0 16px; font-size: 27px; }
    p { margin: 0 0 12px; line-height: 1.6; color: #6f5647; }
    code {
      display: block;
      padding: 16px 20px;
      border-radius: 14px;
      background: #33241c;
      color: #f7e9d8;
      font-size: 14px;
      line-height: 1.7;
      white-space: pre-wrap;
    }
  </style>
</head>
<body>
  <main>
    <h1>看板尚未构建</h1>
    <p>网关已在运行，但还没有找到前端产物。先构建看板，再刷新这个页面。</p>
    <code>cd gateway/web
npm install
npm run build</code>
    <p>开发时也可以运行 <strong>npm run dev</strong>，访问 Vite 提供的 :5173 地址。</p>
  </main>
</body>
</html>
`
