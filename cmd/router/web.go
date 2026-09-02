package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	webDefaultPort = "8088"
	webStartArg    = "__serve"
)

var errWebUsage = errors.New("web usage error")
var errWebStateInvalid = errors.New("Router state is invalid")

type webState struct {
	PID        int       `json:"pid"`
	BinaryPath string    `json:"binary_path"`
	Port       string    `json:"port"`
	StartedAt  time.Time `json:"started_at"`
	LogPath    string    `json:"log_path"`
}

func runWebCommand(args []string) error {
	if len(args) == 0 {
		printWebUsage(os.Stderr)
		return errWebUsage
	}
	if len(args) == 2 && (args[1] == "help" || args[1] == "--help" || args[1] == "-h") {
		printWebUsage(os.Stdout)
		return nil
	}

	switch args[0] {
	case "start":
		return runWebStart(args[1:])
	case "stop":
		return runWebStop(args[1:])
	case "restart":
		return runWebRestart(args[1:])
	case "status":
		return runWebStatus(args[1:])
	case "help", "--help", "-h":
		printWebUsage(os.Stdout)
		return nil
	default:
		printWebUsage(os.Stderr)
		return fmt.Errorf("unknown web command %q", args[0])
	}
}

func printWebUsage(w io.Writer) {
	fmt.Fprintln(w, `Usage:
	  router web start [--port 8088] [--foreground] [--open]
	  router web stop
	  router web restart [--port 8088] [--foreground] [--open]
  router web status`)
}

func runWebStart(args []string) error {
	options, err := parseWebStartFlags("start", args)
	if err != nil {
		return err
	}
	if options.Help {
		printWebUsage(os.Stdout)
		return nil
	}

	if state, ok := liveWebState(); ok {
		return fmt.Errorf("Router is already running (pid %d, http://127.0.0.1:%s)", state.PID, state.Port)
	}
	if err := rejectHealthyWebPort(options.Port); err != nil {
		return err
	}
	if err := removeWebState(); err != nil {
		return err
	}

	state, err := startWebProcess(options)
	if err != nil {
		return err
	}
	if options.Foreground {
		return nil
	}

	fmt.Printf("Router started (pid %d)\n", state.PID)
	fmt.Printf("URL:  http://127.0.0.1:%s\n", state.Port)
	fmt.Printf("Logs: %s\n", state.LogPath)
	if options.Open {
		if err := openWebURL("http://127.0.0.1:" + state.Port + "/ui"); err != nil {
			fmt.Fprintf(os.Stderr, "router: could not open browser: %v\n", err)
		}
	}
	return nil
}

func runWebStop(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("%w: stop does not accept arguments", errWebUsage)
	}
	state, err := readWebState()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Println("Router is not running")
			return nil
		}
		if errors.Is(err, errWebStateInvalid) {
			if removeErr := removeWebState(); removeErr != nil {
				return removeErr
			}
			fmt.Println("Router state was invalid; removed without signaling any process")
			return nil
		}
		return err
	}
	if !processAlive(state.PID) {
		_ = removeWebState()
		fmt.Println("Router is not running")
		return nil
	}

	if err := signalWebProcess(state.PID); err != nil {
		return fmt.Errorf("stop Router process %d: %w", state.PID, err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(state.PID) {
			_ = removeWebState()
			fmt.Println("Router stopped")
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("Router process %d did not stop within 10s", state.PID)
}

func runWebRestart(args []string) error {
	if err := runWebStop(nil); err != nil {
		return err
	}
	return runWebStart(args)
}

func runWebStatus(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("%w: status does not accept arguments", errWebUsage)
	}
	state, err := readWebState()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Println("Status: stopped")
			return nil
		}
		return err
	}
	status := "stopped"
	if processAlive(state.PID) {
		status = "running"
	}
	fmt.Printf("Status:  %s\n", status)
	fmt.Printf("URL:     http://127.0.0.1:%s\n", state.Port)
	fmt.Printf("PID:     %d\n", state.PID)
	fmt.Printf("Logs:    %s\n", state.LogPath)
	fmt.Printf("Started: %s\n", state.StartedAt.Format(time.RFC3339))
	if status == "stopped" {
		_ = removeWebState()
	}
	return nil
}

type webStartOptions struct {
	Port       string
	Foreground bool
	Open       bool
	Help       bool
}

func parseWebStartFlags(name string, args []string) (webStartOptions, error) {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	port := os.Getenv("PORT")
	if port == "" {
		port = webDefaultPort
	}
	options := webStartOptions{Port: port}
	flags.StringVar(&options.Port, "port", options.Port, "HTTP port for the Router service")
	flags.BoolVar(&options.Foreground, "foreground", false, "run in the foreground")
	flags.BoolVar(&options.Open, "open", false, "open the Router UI in the default browser")
	flags.BoolVar(&options.Help, "help", false, "show help")
	if err := flags.Parse(args); err != nil {
		return webStartOptions{}, fmt.Errorf("%w: %v", errWebUsage, err)
	}
	if flags.NArg() != 0 {
		return webStartOptions{}, fmt.Errorf("%w: unexpected argument %q", errWebUsage, flags.Arg(0))
	}
	if err := validateWebPort(options.Port); err != nil {
		return webStartOptions{}, err
	}
	return options, nil
}

func validateWebPort(port string) error {
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("%w: invalid port %q", errWebUsage, port)
	}
	return nil
}

func startWebProcess(options webStartOptions) (webState, error) {
	exe, err := os.Executable()
	if err != nil {
		return webState{}, fmt.Errorf("resolve router executable: %w", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return webState{}, fmt.Errorf("resolve router executable path: %w", err)
	}

	if options.Foreground {
		if err := os.Setenv("PORT", options.Port); err != nil {
			return webState{}, err
		}
		runServer()
		return webState{Port: options.Port}, nil
	}

	stateDir, err := webStateDir()
	if err != nil {
		return webState{}, err
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return webState{}, fmt.Errorf("create Router state directory: %w", err)
	}
	logPath := filepath.Join(stateDir, "web.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return webState{}, fmt.Errorf("open Router log: %w", err)
	}
	defer logFile.Close()

	cmd := exec.Command(exe, webStartArg)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Stdin = nil
	cmd.Env = setEnv(os.Environ(), "PORT", options.Port)
	if uiDir := resolveUIAssetsDir(); uiDir != "" {
		cmd.Env = setEnv(cmd.Env, "ROUTER_UI_ASSETS_DIR", uiDir)
	}
	configureDetachedProcess(cmd)
	if err := cmd.Start(); err != nil {
		return webState{}, fmt.Errorf("start Router process: %w", err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Process.Release(); err != nil {
		return webState{}, fmt.Errorf("detach Router process: %w", err)
	}

	state := webState{
		PID:        pid,
		BinaryPath: exe,
		Port:       options.Port,
		StartedAt:  time.Now().UTC(),
		LogPath:    logPath,
	}
	if err := writeWebState(state); err != nil {
		_ = signalWebProcess(state.PID)
		return webState{}, err
	}
	if err := waitForWebHealth(options.Port, 45*time.Second, state.PID); err != nil {
		_ = signalWebProcess(state.PID)
		_ = removeWebState()
		return webState{}, err
	}
	return state, nil
}

func waitForWebHealth(port string, timeout time.Duration, pid int) error {
	client := &http.Client{Timeout: 1 * time.Second}
	deadline := time.Now().Add(timeout)
	url := "http://127.0.0.1:" + port + "/health"
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return errors.New("Router exited during startup; check router web status and logs")
		}
		response, err := client.Get(url)
		if err == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if response.StatusCode >= 200 && response.StatusCode < 300 {
				return nil
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("Router did not become healthy within %s; check logs: %s", timeout.Round(time.Second), webLogPath())
}

func rejectHealthyWebPort(port string) error {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	response, err := client.Get("http://127.0.0.1:" + port + "/health")
	if err != nil {
		return nil
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return fmt.Errorf("a healthy HTTP service is already running on port %s; stop it before starting Router", port)
	}
	return nil
}

func webStateDir() (string, error) {
	if root := os.Getenv("XDG_STATE_HOME"); root != "" {
		return filepath.Join(root, "weave-router"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".local", "state", "weave-router"), nil
}

func webStatePath() string {
	dir, err := webStateDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "web.json")
}

func webLogPath() string {
	dir, err := webStateDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "web.log")
}

func resolveUIAssetsDir() string {
	if configured := strings.TrimSpace(os.Getenv("ROUTER_UI_ASSETS_DIR")); configured != "" {
		if uiAssetsExist(configured) {
			return configured
		}
	}
	for _, candidate := range []string{
		filepath.Join("assets", "ui"),
		defaultUIAssetsDir,
	} {
		if uiAssetsExist(candidate) {
			absolute, err := filepath.Abs(candidate)
			if err == nil {
				return absolute
			}
		}
	}
	return ""
}

func uiAssetsExist(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(path, "index.html"))
	return err == nil && !info.IsDir()
}

func readWebState() (webState, error) {
	path := webStatePath()
	if path == "" {
		return webState{}, errors.New("resolve Router state path")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return webState{}, err
	}
	var state webState
	if err := json.Unmarshal(data, &state); err != nil {
		return webState{}, fmt.Errorf("read Router state: %w", err)
	}
	if state.PID <= 0 || strings.TrimSpace(state.Port) == "" {
		return webState{}, fmt.Errorf("%w; run router web restart", errWebStateInvalid)
	}
	return state, nil
}

func writeWebState(state webState) error {
	path := webStatePath()
	if path == "" {
		return errors.New("resolve Router state path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create Router state directory: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode Router state: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".web-state-*")
	if err != nil {
		return fmt.Errorf("create Router state: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write Router state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("install Router state: %w", err)
	}
	return nil
}

func removeWebState() error {
	path := webStatePath()
	if path == "" {
		return errors.New("resolve Router state path")
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove Router state: %w", err)
	}
	return nil
}

func liveWebState() (webState, bool) {
	state, err := readWebState()
	return state, err == nil && processAlive(state.PID)
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = process.Signal(syscall.Signal(0))
	return err == nil
}

func openWebURL(url string) error {
	command := "xdg-open"
	args := []string{url}
	if _, err := exec.LookPath("open"); err == nil {
		command = "open"
	} else if _, err := exec.LookPath(command); err != nil {
		return errors.New("neither macOS open nor xdg-open is available")
	}
	return exec.Command(command, args...).Start()
}

func setEnv(env []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			continue
		}
		out = append(out, entry)
	}
	return append(out, prefix+value)
}
