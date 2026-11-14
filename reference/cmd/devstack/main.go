// devstack 一键构建并启动参考版的全部进程。
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

type buildTarget struct {
	name string
	pkg  string
}

type processSpec struct {
	name     string
	bin      string
	args     []string
	env      []string
	readyURL string
	color    string
}

type runningProcess struct {
	spec    processSpec
	proc    *os.Process
	logFile *os.File
	logMu   sync.Mutex
	exited  chan struct{}
	exitErr error
}

func main() {
	root := flag.String("root", ".", "reference project root")
	runFor := flag.Duration("run-for", 0, "automatically stop after this duration (0 = forever)")
	chaos := flag.Bool("chaos", false, "make user-service v2 fail all /users requests")
	skipBuild := flag.Bool("skip-build", false, "reuse existing binaries in .run/bin")
	noEdge := flag.Bool("no-edge", false, "do not start the edge gateway")
	logDir := flag.String("log-dir", ".run/logs", "directory for process logs")
	binDir := flag.String("bin-dir", ".run/bin", "directory for built binaries")
	controlAddr := flag.String("control-addr", "127.0.0.1:18099", "local control endpoint used to request shutdown")
	flag.Parse()

	rootPath, err := filepath.Abs(*root)
	if err != nil {
		fatal("resolve root: %v", err)
	}

	logPath := filepath.Join(rootPath, *logDir)
	binPath := filepath.Join(rootPath, *binDir)

	for _, dir := range []string{logPath, binPath, filepath.Join(rootPath, ".run", "data", "ca")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fatal("create directory %s: %v", dir, err)
		}
	}

	targets := []buildTarget{
		{name: "controlplane", pkg: "./mesh/controlplane"},
		{name: "sidecar", pkg: "./mesh/sidecar"},
		{name: "user-service", pkg: "./apps/user-service"},
		{name: "order-service", pkg: "./apps/order-service"},
		{name: "edge", pkg: "./edge"},
	}

	if !*skipBuild {
		if err := buildAll(rootPath, binPath, targets); err != nil {
			fatal("build failed: %v", err)
		}
	}

	adminPassword := os.Getenv("GATEWAY_ADMIN_PASSWORD")
	if adminPassword == "" {
		adminPassword = "admin-" + randomPassword()
	}
	viewerPassword := os.Getenv("GATEWAY_VIEWER_PASSWORD")
	if viewerPassword == "" {
		viewerPassword = "viewer-" + randomPassword()
	}

	controlPlaneURL := "http://127.0.0.1:18000"
	meshConfigDir := filepath.Join(rootPath, "configs", "mesh")
	gatewayConfig := filepath.Join(rootPath, "configs", "gateway.yaml")
	caDir := filepath.Join(rootPath, ".run", "data", "ca")

	bin := func(name string) string {
		return filepath.Join(binPath, binaryName(name))
	}

	specs := []processSpec{
		{
			name:     "controlplane",
			bin:      bin("controlplane"),
			args:     []string{"--addr", "127.0.0.1:18000", "--config-dir", meshConfigDir, "--data-dir", caDir},
			readyURL: controlPlaneURL + "/health",
			color:    "36",
		},
		{
			name:     "user-v1",
			bin:      bin("user-service"),
			args:     []string{"--addr", "127.0.0.1:9101", "--service", "user-service", "--version", "v1"},
			readyURL: "http://127.0.0.1:9101/healthz",
			color:    "32",
		},
		{
			name: "user-v1-sidecar",
			bin:  bin("sidecar"),
			args: []string{
				"--service", "user-service", "--version", "v1",
				"--app-addr", "127.0.0.1:9101",
				"--inbound", "127.0.0.1:19101",
				"--outbound", "127.0.0.1:15001",
				"--admin", "127.0.0.1:19201",
				"--control-plane", controlPlaneURL,
			},
			readyURL: "http://127.0.0.1:19201/ready",
			color:    "90",
		},
		{
			name:     "user-v2",
			bin:      bin("user-service"),
			args:     []string{"--addr", "127.0.0.1:9102", "--service", "user-service", "--version", "v2", "--fail-rate", boolFloat(*chaos)},
			readyURL: "http://127.0.0.1:9102/healthz",
			color:    "32",
		},
		{
			name: "user-v2-sidecar",
			bin:  bin("sidecar"),
			args: []string{
				"--service", "user-service", "--version", "v2",
				"--app-addr", "127.0.0.1:9102",
				"--inbound", "127.0.0.1:19102",
				"--outbound", "127.0.0.1:15002",
				"--admin", "127.0.0.1:19202",
				"--control-plane", controlPlaneURL,
			},
			readyURL: "http://127.0.0.1:19202/ready",
			color:    "90",
		},
		{
			name:     "order",
			bin:      bin("order-service"),
			args:     []string{"--addr", "127.0.0.1:9103", "--sidecar", "127.0.0.1:15003"},
			readyURL: "http://127.0.0.1:9103/healthz",
			color:    "33",
		},
		{
			name: "order-sidecar",
			bin:  bin("sidecar"),
			args: []string{
				"--service", "order-service", "--version", "v1",
				"--app-addr", "127.0.0.1:9103",
				"--inbound", "127.0.0.1:19103",
				"--outbound", "127.0.0.1:15003",
				"--admin", "127.0.0.1:19203",
				"--control-plane", controlPlaneURL,
			},
			readyURL: "http://127.0.0.1:19203/ready",
			color:    "90",
		},
	}

	if !*noEdge {
		specs = append(specs, processSpec{
			name: "edge",
			bin:  bin("edge"),
			args: []string{"--config", gatewayConfig},
			env: []string{
				"GATEWAY_ADMIN_PASSWORD=" + adminPassword,
				"GATEWAY_VIEWER_PASSWORD=" + viewerPassword,
			},
			readyURL: "http://127.0.0.1:8080/ready",
			color:    "35",
		})
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *runFor > 0 {
		timer := time.AfterFunc(*runFor, stop)
		defer timer.Stop()
	}

	var processes []*runningProcess

	for _, spec := range specs {
		if err := validateProcessSpec(spec, binPath); err != nil {
			shutdown(processes)
			fatal("invalid process spec: %v", err)
		}

		process, err := startProcess(rootPath, logPath, spec)
		if err != nil {
			shutdown(processes)
			fatal("start %s: %v", spec.name, err)
		}

		processes = append(processes, process)

		if err := waitReady(process, 30*time.Second); err != nil {
			shutdown(processes)
			fatal("%v", err)
		}
	}

	printSummary(adminPassword, viewerPassword, *chaos)

	controlServer := startControlServer(*controlAddr, stop)
	defer controlServer.Close()

	go func() {
		for _, process := range processes {
			go func(p *runningProcess) {
				<-p.exited
				if ctx.Err() == nil {
					fmt.Printf("\n[devstack] %s exited unexpectedly: %v\n", p.spec.name, p.exitErr)
					stop()
				}
			}(process)
		}
	}()

	<-ctx.Done()

	fmt.Println("\n[devstack] shutting down")
	shutdown(processes)
	fmt.Println("[devstack] stopped")
}

func buildAll(root, binDir string, targets []buildTarget) error {
	goBinary, err := findGoBinary()
	if err != nil {
		return err
	}

	for _, target := range targets {
		output := filepath.Join(binDir, binaryName(target.name))

		fmt.Printf("[build] %s -> %s\n", target.name, output)

		argv := []string{goBinary, "build", "-o", output, target.pkg}

		process, err := os.StartProcess(goBinary, argv, &os.ProcAttr{
			Dir:   root,
			Env:   os.Environ(),
			Files: []*os.File{os.Stdin, os.Stdout, os.Stderr},
		})
		if err != nil {
			return fmt.Errorf("build %s: %w", target.name, err)
		}

		state, err := process.Wait()
		if err != nil {
			return fmt.Errorf("build %s: %w", target.name, err)
		}
		if !state.Success() {
			return fmt.Errorf("build %s: exit status %d", target.name, state.ExitCode())
		}
	}

	return nil
}

func startProcess(root, logDir string, spec processSpec) (*runningProcess, error) {
	logFile, err := os.Create(filepath.Join(logDir, spec.name+".log"))
	if err != nil {
		return nil, err
	}

	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		_ = logFile.Close()
		return nil, err
	}

	stderrReader, stderrWriter, err := os.Pipe()
	if err != nil {
		_ = stdoutReader.Close()
		_ = stdoutWriter.Close()
		_ = logFile.Close()
		return nil, err
	}

	argv := append([]string{spec.bin}, spec.args...)

	process, err := os.StartProcess(spec.bin, argv, &os.ProcAttr{
		Dir:   root,
		Env:   mergeEnv(os.Environ(), spec.env),
		Files: []*os.File{os.Stdin, stdoutWriter, stderrWriter},
	})
	if err != nil {
		_ = stdoutReader.Close()
		_ = stdoutWriter.Close()
		_ = stderrReader.Close()
		_ = stderrWriter.Close()
		_ = logFile.Close()
		return nil, err
	}

	_ = stdoutWriter.Close()
	_ = stderrWriter.Close()

	running := &runningProcess{
		spec:    spec,
		proc:    process,
		logFile: logFile,
		exited:  make(chan struct{}),
	}

	go running.stream(stdoutReader)
	go running.stream(stderrReader)

	go func() {
		state, waitErr := process.Wait()

		if waitErr == nil && state != nil && !state.Success() {
			waitErr = fmt.Errorf("exit status %d", state.ExitCode())
		}

		running.exitErr = waitErr
		close(running.exited)
	}()

	return running, nil
}

func (p *runningProcess) stream(reader io.Reader) {
	if closer, ok := reader.(io.Closer); ok {
		defer closer.Close()
	}

	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)

	for scanner.Scan() {
		line := scanner.Text()

		fmt.Printf("\x1b[%sm[%s]\x1b[0m %s\n", p.spec.color, p.spec.name, line)

		p.logMu.Lock()
		_, _ = p.logFile.WriteString(line + "\n")
		p.logMu.Unlock()
	}
}

func waitReady(process *runningProcess, timeout time.Duration) error {
	if process.spec.readyURL == "" {
		time.Sleep(300 * time.Millisecond)
		return nil
	}

	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(timeout)

	for {
		select {
		case <-process.exited:
			return fmt.Errorf("%s exited before becoming ready: %v", process.spec.name, process.exitErr)
		default:
		}

		response, err := client.Get(process.spec.readyURL)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				fmt.Printf("\x1b[%sm[%s]\x1b[0m ready at %s\n", process.spec.color, process.spec.name, process.spec.readyURL)
				return nil
			}
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for %s at %s", process.spec.name, process.spec.readyURL)
		}

		time.Sleep(200 * time.Millisecond)
	}
}

func shutdown(processes []*runningProcess) {
	for i := len(processes) - 1; i >= 0; i-- {
		process := processes[i]

		select {
		case <-process.exited:
			_ = process.logFile.Close()
			continue
		default:
		}

		fmt.Printf("[devstack] stopping %s\n", process.spec.name)

		if runtime.GOOS == "windows" {
			_ = process.proc.Kill()
		} else {
			_ = process.proc.Signal(os.Interrupt)
		}

		select {
		case <-process.exited:
		case <-time.After(5 * time.Second):
			_ = process.proc.Kill()
			<-process.exited
		}

		_ = process.logFile.Close()
	}
}

// startControlServer 提供一个本地关闭入口，便于脚本可靠地停止整棵进程树。
func startControlServer(addr string, stop func()) *http.Server {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	mux.HandleFunc("POST /shutdown", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
		go stop()
	})

	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Printf("[devstack] control endpoint failed: %v\n", err)
		}
	}()

	return server
}

func printSummary(adminPassword, viewerPassword string, chaos bool) {
	fmt.Println()
	fmt.Println("============================================================")
	fmt.Println(" Go Gateway Reference · dev stack")
	fmt.Println("------------------------------------------------------------")
	fmt.Println(" Edge Gateway   http://127.0.0.1:8080/")
	fmt.Printf("   admin / %s\n", adminPassword)
	fmt.Printf("   viewer / %s\n", viewerPassword)
	fmt.Println(" Control Plane  http://127.0.0.1:18000/")
	fmt.Println("------------------------------------------------------------")
	fmt.Println(" Mesh instances")
	fmt.Println("   user-service v1  sidecar inbound 127.0.0.1:19101  outbound 15001")
	fmt.Println("   user-service v2  sidecar inbound 127.0.0.1:19102  outbound 15002")
	fmt.Println("   order-service    sidecar inbound 127.0.0.1:19103  outbound 15003")
	if chaos {
		fmt.Println("   chaos: user-service v2 returns 500 for /users/*")
	}
	fmt.Println("------------------------------------------------------------")
	fmt.Println(" Try")
	fmt.Printf("   curl -s -X POST http://127.0.0.1:8080/auth/login \\\n")
	fmt.Printf("     -H 'Content-Type: application/json' \\\n")
	fmt.Printf("     -d '{\"username\":\"admin\",\"password\":\"%s\"}'\n", adminPassword)
	fmt.Println("   curl -x http://127.0.0.1:15003 http://user-service/users/1")
	fmt.Println("   curl -k https://127.0.0.1:19101/users/1")
	fmt.Println("============================================================")
	fmt.Println()
}

func mergeEnv(base, overrides []string) []string {
	values := make(map[string]string, len(base)+len(overrides))
	order := make([]string, 0, len(base)+len(overrides))

	for _, item := range append(append([]string(nil), base...), overrides...) {
		key, _, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}

		if _, exists := values[key]; !exists {
			order = append(order, key)
		}
		values[key] = item
	}

	out := make([]string, 0, len(order))
	for _, key := range order {
		out = append(out, values[key])
	}

	return out
}

func findGoBinary() (string, error) {
	candidate := filepath.Join(runtime.GOROOT(), "bin", binaryName("go"))
	if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
		return candidate, nil
	}

	return "", fmt.Errorf("go binary not found under GOROOT %s", runtime.GOROOT())
}

func validateProcessSpec(spec processSpec, binDir string) error {
	absoluteBin, err := filepath.Abs(spec.bin)
	if err != nil {
		return err
	}

	absoluteDir, err := filepath.Abs(binDir)
	if err != nil {
		return err
	}

	relative, err := filepath.Rel(absoluteDir, absoluteBin)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("binary %s is outside %s", absoluteBin, absoluteDir)
	}

	name := strings.TrimSuffix(filepath.Base(absoluteBin), ".exe")

	switch name {
	case "controlplane", "sidecar", "user-service", "order-service", "edge":
		return nil
	default:
		return fmt.Errorf("unexpected binary name %q", name)
	}
}

func binaryName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

func randomPassword() string {
	buffer := make([]byte, 6)
	if _, err := rand.Read(buffer); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buffer)
}

func boolFloat(value bool) string {
	if value {
		return "1"
	}
	return "0"
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[devstack] "+format+"\n", args...)
	os.Exit(1)
}
