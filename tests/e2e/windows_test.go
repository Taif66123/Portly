//go:build windows

// The Windows evidence gate: real stops of servers this test starts. Windows has no graceful stop,
// so every stop is a kill that needs --force. The release build must still refuse everything.
package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	modeVariable   = "PORTLY_E2E_MODE"
	binaryVariable = "PORTLY_E2E_BINARY"
	// Every server ends by itself, even if its test crashes
	serverLifetime = 90 * time.Second
)

// The binary users get, and one that may stop this test's children
var release, evidence string

// TestMain builds both binaries, or becomes a server when asked.
func TestMain(m *testing.M) {
	if mode := os.Getenv(modeVariable); mode != "" {
		serve(mode)
		return
	}
	// Every server and its children end with this test, whatever happens to it
	if err := killWithMe(); err != nil {
		fmt.Fprintln(os.Stderr, "job:", err)
		os.Exit(1)
	}
	dir, err := os.MkdirTemp("", "portly-e2e")
	if err == nil {
		release, err = build(dir, "portly.exe")
	}
	if err == nil {
		evidence, err = build(dir, "portly-evidence.exe", "-tags", "portly_evidence")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "build:", err)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// killWithMe puts this process in a job that ends all its descendants when it exits.
func killWithMe() error {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return err
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	_, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits)))
	if err != nil {
		return err
	}
	// The handle stays open on purpose: it closes when this process ends
	return windows.AssignProcessToJobObject(job, windows.CurrentProcess())
}

// build compiles one binary into a temporary directory.
func build(dir, name string, flags ...string) (string, error) {
	path := filepath.Join(dir, name)
	arguments := append([]string{"build", "-o", path}, flags...)
	command := exec.Command("go", append(arguments, "../../cmd/portly")...)
	command.Env = append(os.Environ(), "CGO_ENABLED=0")
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, output)
	}
	return path, nil
}

// server is a child process listening on one or more ports.
type server struct {
	command *exec.Cmd
	ports   []string
	exited  chan struct{}
}

// start launches this test binary as a server.
func start(t *testing.T, mode string, extra ...string) *server {
	t.Helper()
	return startAs(t, os.Args[0], mode, extra...)
}

// startAs launches a copy of this test binary under another program name.
func startAs(t *testing.T, program, mode string, extra ...string) *server {
	t.Helper()
	command := exec.Command(program)
	command.Env = append(append(os.Environ(), modeVariable+"="+mode, binaryVariable+"="+os.Args[0]), extra...)
	return launch(t, command)
}

// launch starts any command that prints its ports, and kills only that child.
func launch(t *testing.T, command *exec.Cmd) *server {
	t.Helper()
	command.Stderr = os.Stderr
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	s := &server{command: command, exited: make(chan struct{})}
	line, err := bufio.NewReader(output).ReadString('\n')
	if err != nil {
		t.Fatalf("server did not start: %v", err)
	}
	s.ports = strings.Fields(line)
	go func() {
		_ = command.Wait()
		close(s.exited)
	}()
	t.Cleanup(func() {
		_ = command.Process.Kill()
		<-s.exited
	})
	return s
}

// alive reports whether the server itself still runs.
func (s *server) alive() bool {
	select {
	case <-s.exited:
		return false
	case <-time.After(50 * time.Millisecond):
		return true
	}
}

// pid is the server's process ID.
func (s *server) pid() int { return s.command.Process.Pid }

// copyAs copies this test binary under another name, since symbolic links need privileges on Windows.
func copyAs(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	source, err := os.Open(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	target, err := os.Create(path)
	if err == nil {
		_, err = io.Copy(target, source)
	}
	if closeErr := target.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// result is one run of portly.
type result struct {
	exit   int
	stdout string
	stderr string
	json   document
}

// document is the part of the JSON output the tests check.
type document struct {
	PlanID  string `json:"plan_id"`
	Error   *problem
	Ports   []map[string]any
	Targets []struct {
		Kind     string
		Affected []struct{ Port int }
		Outcome  *struct {
			Status, Method string
			KilledPIDs     []int `json:"killed_pids"`
		}
	}
	Results []struct {
		Port     int
		Protocol string
		Status   string
		Error    *problem
	}
}

// problem is an error in the JSON output.
type problem struct{ Code, Message, Hint string }

// portly runs a binary without a terminal.
func portly(t *testing.T, binary string, args ...string) result {
	t.Helper()
	var stdout, stderr strings.Builder
	command := exec.Command(binary, args...)
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	var exited *exec.ExitError
	if err != nil && !errors.As(err, &exited) {
		t.Fatal(err)
	}
	found := result{exit: command.ProcessState.ExitCode(), stdout: stdout.String(), stderr: stderr.String()}
	if strings.HasPrefix(found.stdout, "{") {
		if err := json.Unmarshal([]byte(found.stdout), &found.json); err != nil {
			t.Fatalf("%v\n%s", err, found.stdout)
		}
	}
	return found
}

// kill runs a confirmed forced stop with the evidence build.
func kill(t *testing.T, args ...string) result {
	t.Helper()
	return portly(t, evidence, append([]string{"stop", "--yes", "--force", "--json"}, args...)...)
}

// expect fails unless exit status and first result match.
func (r result) expect(t *testing.T, exit int, status string) {
	t.Helper()
	if r.exit != exit || len(r.json.Results) == 0 || r.json.Results[0].Status != status {
		t.Fatalf("exit %d, want %d %s\n%s%s", r.exit, exit, status, r.stdout, r.stderr)
	}
}

// refused fails unless the first result is refused with this code.
func (r result) refused(t *testing.T, code string) {
	t.Helper()
	exit := 5
	if code == "descendant_unreadable" {
		exit = 4
	}
	if r.exit != exit || len(r.json.Results) == 0 || r.json.Results[0].Error == nil || r.json.Results[0].Error.Code != code {
		t.Fatalf("exit %d, want %d %s\n%s%s", r.exit, exit, code, r.stdout, r.stderr)
	}
}

// isFree reports whether a TCP port can be bound again.
func isFree(port string) bool {
	listener, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		return false
	}
	_ = listener.Close()
	return true
}

// freePort is a TCP port nothing listens on.
func freePort(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return portOf(listener)
}

// serve is the server side of every test.
func serve(mode string) {
	go func() {
		time.Sleep(serverLifetime)
		os.Exit(9)
	}()
	switch mode {
	case "tcp":
		announce(listen("127.0.0.1:0"))
	case "two-ports":
		announce(listen("127.0.0.1:0"), listen("127.0.0.1:0"))
	case "dual":
		first := listen("127.0.0.1:0")
		announce(first, listen("[::1]:"+portOf(first)))
	case "udp":
		conn, err := net.ListenPacket("udp", "127.0.0.1:"+os.Getenv("PORTLY_E2E_PORT"))
		check(err)
		_, port, _ := net.SplitHostPort(conn.LocalAddr().String())
		fmt.Println(port)
	case "share":
		announce(reuse("127.0.0.1:" + os.Getenv("PORTLY_E2E_PORT")))
	case "with-child":
		// Like a reloader: the child may have inherited the socket, and Windows can't tell
		listener := listen("127.0.0.1:0")
		helper := exec.Command(os.Args[0])
		helper.Env = append(os.Environ(), modeVariable+"=idle")
		check(helper.Start())
		fmt.Println(portOf(listener), helper.Process.Pid)
	case "parent":
		// The child is the real test binary, so only the parent carries the chosen name
		server := exec.Command(os.Getenv(binaryVariable))
		server.Env = append(os.Environ(), modeVariable+"=tcp")
		server.Stdout, server.Stderr = os.Stdout, os.Stderr
		check(server.Start())
		_ = server.Wait()
		os.Exit(0)
	}
	select {}
}

// check ends a server that can't run.
func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// listen opens a TCP port.
func listen(address string) net.Listener {
	listener, err := net.Listen("tcp", address)
	check(err)
	return listener
}

// reuse opens a TCP port that another process may also bind.
func reuse(address string) net.Listener {
	config := net.ListenConfig{Control: func(_, _ string, conn syscall.RawConn) error {
		return conn.Control(func(fd uintptr) {
			check(windows.SetsockoptInt(windows.Handle(fd), windows.SOL_SOCKET, windows.SO_REUSEADDR, 1))
		})
	}}
	listener, err := config.Listen(context.Background(), "tcp", address)
	check(err)
	return listener
}

// portOf is the port of a listener.
func portOf(listener net.Listener) string {
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	return port
}

// announce prints every port on one line.
func announce(listeners ...net.Listener) {
	var ports []string
	for _, listener := range listeners {
		ports = append(ports, portOf(listener))
	}
	fmt.Println(strings.Join(ports, " "))
}

// TestWindowsReleaseBuildRefuses: the build users get keeps the gate closed, even with --force.
func TestWindowsReleaseBuildRefuses(t *testing.T) {
	s := start(t, "tcp")
	portly(t, release, "stop", s.ports[0], "--yes", "--force", "--json").refused(t, "platform_unverified")
	if !s.alive() {
		t.Error("the release build stopped a process")
	}
}

// TestWindowsNeedsForce: there is no graceful stop, so nothing happens without --force.
func TestWindowsNeedsForce(t *testing.T) {
	s := start(t, "tcp")
	portly(t, evidence, "stop", s.ports[0], "--yes", "--json").refused(t, "no_graceful_method")
	if !s.alive() {
		t.Error("stopped without --force")
	}
}

// TestWindowsNothingStopsWithoutConfirmation covers pipes, JSON and force without --yes.
func TestWindowsNothingStopsWithoutConfirmation(t *testing.T) {
	s := start(t, "tcp")
	if got := portly(t, evidence, "stop", s.ports[0], "--force", "--json"); got.exit != 6 || !s.alive() {
		t.Errorf("exit %d, alive %v\n%s", got.exit, s.alive(), got.stdout)
	}
	if got := portly(t, evidence, "stop", s.ports[0], "--force"); got.exit != 6 || !s.alive() {
		t.Errorf("exit %d, alive %v\n%s%s", got.exit, s.alive(), got.stdout, got.stderr)
	}
}

// TestWindowsForceKills ends exactly the server and frees its port.
func TestWindowsForceKills(t *testing.T) {
	s := start(t, "tcp")
	got := kill(t, s.ports[0])
	got.expect(t, 0, "stopped")
	outcome := got.json.Targets[0].Outcome
	if outcome.Method != "forced" || !slices.Equal(outcome.KilledPIDs, []int{s.pid()}) || s.alive() || !isFree(s.ports[0]) {
		t.Errorf("%+v alive %v", outcome, s.alive())
	}
}

// TestWindowsDualStackIsOneTarget kills once for IPv4 and IPv6.
func TestWindowsDualStackIsOneTarget(t *testing.T) {
	s := start(t, "dual")
	got := kill(t, s.ports[0])
	got.expect(t, 0, "stopped")
	if len(got.json.Targets) != 1 || s.alive() {
		t.Errorf("%d targets, alive %v", len(got.json.Targets), s.alive())
	}
}

// TestWindowsTwoOwnersAreRefused leaves both running.
func TestWindowsTwoOwnersAreRefused(t *testing.T) {
	first := start(t, "share")
	second := start(t, "share", "PORTLY_E2E_PORT="+first.ports[0])
	kill(t, first.ports[0]).refused(t, "ambiguous_owner")
	if !first.alive() || !second.alive() {
		t.Error("an owner of a shared port was killed")
	}
}

// TestWindowsUDPNeedsToBeNamed proves a bare port never stops UDP.
func TestWindowsUDPNeedsToBeNamed(t *testing.T) {
	s := start(t, "udp")
	got := kill(t, s.ports[0])
	got.expect(t, 3, "not_listening")
	if !s.alive() || !strings.Contains(got.json.Results[0].Error.Hint, "/udp") {
		t.Errorf("alive %v, %+v", s.alive(), got.json.Results[0].Error)
	}
	kill(t, s.ports[0]+"/udp").expect(t, 0, "stopped")
}

// TestWindowsSameNumberTCPAndUDP never mixes the two.
func TestWindowsSameNumberTCPAndUDP(t *testing.T) {
	tcp := start(t, "tcp")
	udp := start(t, "udp", "PORTLY_E2E_PORT="+tcp.ports[0])
	kill(t, tcp.ports[0]).expect(t, 0, "stopped")
	if !udp.alive() {
		t.Error("the UDP owner was killed")
	}
}

// TestWindowsSeveralPorts covers success, a mix, duplicates and a shared owner.
func TestWindowsSeveralPorts(t *testing.T) {
	first, second, both := start(t, "tcp"), start(t, "tcp"), start(t, "two-ports")
	got := kill(t, first.ports[0], second.ports[0], first.ports[0])
	if got.exit != 0 || len(got.json.Results) != 2 || first.alive() || second.alive() {
		t.Errorf("two: exit %d\n%s", got.exit, got.stdout)
	}
	got = kill(t, both.ports[0], both.ports[1], first.ports[0])
	if got.exit != 8 || len(got.json.Targets) != 1 || got.json.Results[2].Status != "not_listening" || both.alive() {
		t.Errorf("mixed: exit %d\n%s", got.exit, got.stdout)
	}
}

// TestWindowsOtherEndpointsAreShown before anything stops.
func TestWindowsOtherEndpointsAreShown(t *testing.T) {
	s := start(t, "two-ports")
	got := portly(t, evidence, "stop", s.ports[0], "--force", "--json")
	if len(got.json.Targets) != 1 || len(got.json.Targets[0].Affected) != 1 || strconv.Itoa(got.json.Targets[0].Affected[0].Port) != s.ports[1] {
		t.Errorf("%s", got.stdout)
	}
}

// TestWindowsExpect accepts the same target and refuses a replaced one, so a reused PID is never killed.
func TestWindowsExpect(t *testing.T) {
	s := start(t, "share")
	id := portly(t, evidence, "stop", s.ports[0], "--force", "--json").json.PlanID
	if len(id) != 32 {
		t.Fatalf("plan id %q", id)
	}
	_ = s.command.Process.Kill()
	<-s.exited
	replacement := start(t, "share", "PORTLY_E2E_PORT="+s.ports[0])
	got := kill(t, s.ports[0], "--expect", id)
	if got.exit != 5 || got.json.Error == nil || got.json.Error.Code != "target_changed" || !replacement.alive() {
		t.Errorf("exit %d\n%s", got.exit, got.stdout)
	}
	id = portly(t, evidence, "stop", s.ports[0], "--force", "--json").json.PlanID
	kill(t, s.ports[0], "--expect", id).expect(t, 0, "stopped")
}

// TestWindowsChildMayHoldTheSocket: Windows names only a socket's creator, so an owner with children is refused.
func TestWindowsChildMayHoldTheSocket(t *testing.T) {
	s := start(t, "with-child")
	child, err := os.FindProcess(atoi(t, s.ports[1]))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Kill() })
	kill(t, s.ports[0]).refused(t, "descendant_unreadable")
	if !s.alive() || isFree(s.ports[0]) {
		t.Error("an owner with a child was killed")
	}
}

// TestWindowsOwnAncestorIsProtected covers a port of the process that launched Portly.
func TestWindowsOwnAncestorIsProtected(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	kill(t, portOf(listener)).refused(t, "protected_process")
}

// TestWindowsSupervisorIsRespected covers a parent that would restart its child.
func TestWindowsSupervisorIsRespected(t *testing.T) {
	s := startAs(t, copyAs(t, "nodemon.exe"), "parent")
	kill(t, s.ports[0]).refused(t, "supervised_process")
	if !s.alive() {
		t.Error("the supervisor was killed")
	}
}

// TestWindowsForwarderIsRefused covers a process that only forwards.
func TestWindowsForwarderIsRefused(t *testing.T) {
	s := startAs(t, copyAs(t, "docker-proxy.exe"), "tcp")
	kill(t, s.ports[0]).refused(t, "port_forwarder")
	if !s.alive() {
		t.Error("the forwarder was killed")
	}
}

// TestWindowsLookup finds the server and then a free port.
func TestWindowsLookup(t *testing.T) {
	s := start(t, "tcp")
	got := portly(t, release, s.ports[0], "--json")
	if got.exit != 0 || got.json.Ports[0]["status"] != "listening" || !strings.Contains(got.stdout, strconv.Itoa(s.pid())) {
		t.Errorf("exit %d\n%s", got.exit, got.stdout)
	}
	_ = s.command.Process.Kill()
	<-s.exited
	if got := portly(t, release, s.ports[0], "--json"); got.exit != 0 || got.json.Ports[0]["status"] != "not_listening" {
		t.Errorf("exit %d\n%s", got.exit, got.stdout)
	}
}

// TestWindowsRepeatedStops covers PID churn, one kill at a time.
func TestWindowsRepeatedStops(t *testing.T) {
	cycles, err := strconv.Atoi(os.Getenv("PORTLY_STOP_CYCLES"))
	if err != nil {
		cycles = 5
	}
	for range cycles {
		s := start(t, "tcp")
		kill(t, s.ports[0]).expect(t, 0, "stopped")
	}
}

// gateServer runs a real server in a directory and waits for its port.
func gateServer(t *testing.T, program string, files map[string]string, arguments ...string) string {
	t.Helper()
	path, err := exec.LookPath(program)
	if err != nil {
		t.Skipf("gate not proven here: %s is not installed", program)
	}
	dir, port := t.TempDir(), freePort(t)
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for i, argument := range arguments {
		arguments[i] = strings.ReplaceAll(argument, "PORT", port)
	}
	command := exec.Command(path, arguments...)
	command.Dir = dir
	command.Env = append(os.Environ(), "PORT="+port, "PYTHONUNBUFFERED=1")
	command.Stdout, command.Stderr = os.Stderr, os.Stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// The whole tree, so reloader children leave the directory before it is removed
		_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(command.Process.Pid)).Run()
		_ = command.Process.Kill()
		_ = command.Wait()
	})
	for range 300 {
		if !isFree(port) {
			// Reloaders start their worker after the port opens
			time.Sleep(time.Second)
			return port
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s did not listen", program)
	return ""
}

// held fails if a refused server's port was freed.
func held(t *testing.T, port string) {
	t.Helper()
	if isFree(port) {
		t.Errorf("port %s was freed by a refused stop", port)
	}
}

// atoi reads a number a server printed.
func atoi(t *testing.T, text string) int {
	t.Helper()
	number, err := strconv.Atoi(text)
	if err != nil {
		t.Fatal(err)
	}
	return number
}

// requires skips a gate test when a Python module is missing.
func requires(t *testing.T, module string) {
	t.Helper()
	if exec.Command("python", "-c", "import "+module).Run() != nil {
		t.Skipf("gate not proven here: %s is not installed", module)
	}
}

// gone fails unless the port is free shortly after the kill.
func gone(t *testing.T, port string) {
	t.Helper()
	for range 50 {
		if isFree(port) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Errorf("port %s is still in use", port)
}

// TestGateWindowsNode kills a plain Node server.
func TestGateWindowsNode(t *testing.T) {
	port := gateServer(t, "node", map[string]string{"s.js": `require('http').createServer((q,r)=>r.end('ok')).listen(process.env.PORT,'127.0.0.1');setTimeout(()=>process.exit(9),60000)`}, "s.js")
	kill(t, port).expect(t, 0, "stopped")
	gone(t, port)
}

// TestGateWindowsNodeCluster: the workers share the primary's socket unseen, so it is refused.
func TestGateWindowsNodeCluster(t *testing.T) {
	port := gateServer(t, "node", map[string]string{"s.js": `const c=require('cluster');
if(c.isPrimary){c.fork();c.fork();setTimeout(()=>process.exit(9),60000)}
else{require('http').createServer((q,r)=>r.end('ok')).listen(process.env.PORT,'127.0.0.1')}`}, "s.js")
	kill(t, port).refused(t, "descendant_unreadable")
	held(t, port)
}

// TestGateWindowsPythonHTTP kills python -m http.server.
func TestGateWindowsPythonHTTP(t *testing.T) {
	requires(t, "http.server")
	port := gateServer(t, "python", nil, "-m", "http.server", "PORT", "--bind", "127.0.0.1")
	kill(t, port).expect(t, 0, "stopped")
	gone(t, port)
}

// TestGateWindowsFlask kills the development server.
func TestGateWindowsFlask(t *testing.T) {
	requires(t, "flask")
	files := map[string]string{"app.py": "from flask import Flask\napp = Flask(__name__)\n"}
	port := gateServer(t, "python", files, "-m", "flask", "run", "--port", "PORT")
	kill(t, port).expect(t, 0, "stopped")
	gone(t, port)
}

// TestGateWindowsFlaskReload: the reloader creates the socket and its child inherits it, so it is refused.
func TestGateWindowsFlaskReload(t *testing.T) {
	requires(t, "flask")
	files := map[string]string{"app.py": "from flask import Flask\napp = Flask(__name__)\n"}
	port := gateServer(t, "python", files, "-m", "flask", "run", "--debug", "--port", "PORT")
	kill(t, port).refused(t, "descendant_unreadable")
	held(t, port)
}

// TestGateWindowsDjango kills runserver through its reloader's child.
func TestGateWindowsDjango(t *testing.T) {
	requires(t, "django")
	files := map[string]string{"m.py": "import sys\nfrom django.conf import settings\nsettings.configure(DEBUG=True, ROOT_URLCONF=__name__)\n" +
		"urlpatterns = []\nfrom django.core.management import execute_from_command_line\nexecute_from_command_line(sys.argv)\n"}
	port := gateServer(t, "python", files, "m.py", "runserver", "PORT")
	kill(t, port).expect(t, 0, "stopped")
	gone(t, port)
}

// TestGateWindowsUvicorn kills a single uvicorn server.
func TestGateWindowsUvicorn(t *testing.T) {
	requires(t, "uvicorn")
	files := map[string]string{"app.py": "async def app(scope, receive, send):\n    pass\n"}
	port := gateServer(t, "python", files, "-m", "uvicorn", "app:app", "--port", "PORT")
	kill(t, port).expect(t, 0, "stopped")
	gone(t, port)
}

// TestGateWindowsUvicornReload: the reloader and its worker share one socket, so it is refused.
func TestGateWindowsUvicornReload(t *testing.T) {
	requires(t, "uvicorn")
	files := map[string]string{"app.py": "async def app(scope, receive, send):\n    pass\n"}
	port := gateServer(t, "python", files, "-m", "uvicorn", "app:app", "--reload", "--port", "PORT")
	kill(t, port).refused(t, "descendant_unreadable")
	held(t, port)
}
