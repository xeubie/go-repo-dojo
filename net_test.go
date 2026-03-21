//go:build net

package repomofo

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cgi"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// transportKind represents the network transport protocol.
type transportKind int

const (
	transportHTTP transportKind = iota
	transportRaw
	transportSSH
)

// testServer abstracts the differences between HTTP, raw git, and SSH servers.
type testServer struct {
	kind    transportKind
	port    int
	tempDir string

	// HTTP
	httpListener net.Listener
	httpServer   *http.Server

	// raw / ssh
	process *exec.Cmd
}

func newTestServer(kind transportKind, port int, tempDir string) *testServer {
	return &testServer{
		kind:    kind,
		port:    port,
		tempDir: tempDir,
	}
}

func (s *testServer) init(t *testing.T) {
	t.Helper()
	switch s.kind {
	case transportHTTP:
		addr := fmt.Sprintf("127.0.0.1:%d", s.port)
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			t.Fatalf("listen on %s failed: %v", addr, err)
		}
		s.httpListener = ln

		absTempDir, err := filepath.Abs(s.tempDir)
		if err != nil {
			t.Fatalf("abs temp dir failed: %v", err)
		}

		backendPath := gitHTTPBackendPath()
		mux := http.NewServeMux()
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			pathTranslated := filepath.Join(absTempDir, r.URL.Path)

			handler := &cgi.Handler{
				Path: backendPath,
				Env: []string{
					"GIT_PROJECT_ROOT=" + absTempDir,
					"GIT_HTTP_EXPORT_ALL=1",
					"PATH_TRANSLATED=" + pathTranslated,
				},
			}
			handler.ServeHTTP(w, r)
		})

		s.httpServer = &http.Server{Handler: mux}

	case transportSSH:
		absTempDir, err := filepath.Abs(s.tempDir)
		if err != nil {
			t.Fatalf("abs temp dir failed: %v", err)
		}

		hostKeyPath := filepath.Join(absTempDir, "host_key")
		authKeysPath := filepath.Join(absTempDir, "authorized_keys")

		// create host key
		writeTestFile(t, s.tempDir, "host_key",
			"-----BEGIN OPENSSH PRIVATE KEY-----\n"+
				"b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAaAAAABNlY2RzYS\n"+
				"1zaGEyLW5pc3RwMjU2AAAACG5pc3RwMjU2AAAAQQS1ppUfk8n7yvVKEgz3tXjt4q76VGuj\n"+
				"LcQlRwmogzovV40LLcX0aTObZlQaLWfzJMNpCa/ztMpQlr86nsarE4lEAAAAqLe43zK3uN\n"+
				"8yAAAAE2VjZHNhLXNoYTItbmlzdHAyNTYAAAAIbmlzdHAyNTYAAABBBLWmlR+TyfvK9UoS\n"+
				"DPe1eO3irvpUa6MtxCVHCaiDOi9XjQstxfRpM5tmVBotZ/Mkw2kJr/O0ylCWvzqexqsTiU\n"+
				"QAAAAgQ+LCk30ZNJxb2Da5JL+QOFWCMf7bgXCWcEzhEGGvFWYAAAALcmFkYXJAcm9hcmsB\n"+
				"AgMEBQ==\n"+
				"-----END OPENSSH PRIVATE KEY-----\n")
		os.Chmod(hostKeyPath, 0o600)

		// create client private key
		writeTestFile(t, s.tempDir, "key",
			"-----BEGIN OPENSSH PRIVATE KEY-----\n"+
				"b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW\n"+
				"QyNTUxOQAAACCniLPJiaooAWecvOCeAjoJwCSeWxzysvpTNkpYjF22JgAAAJA+7hikPu4Y\n"+
				"pAAAAAtzc2gtZWQyNTUxOQAAACCniLPJiaooAWecvOCeAjoJwCSeWxzysvpTNkpYjF22Jg\n"+
				"AAAEDVlopOMnKt/7by/IA8VZvQXUS/O6VLkixOqnnahUdPCKeIs8mJqigBZ5y84J4COgnA\n"+
				"JJ5bHPKy+lM2SliMXbYmAAAAC3JhZGFyQHJvYXJrAQI=\n"+
				"-----END OPENSSH PRIVATE KEY-----\n")
		os.Chmod(filepath.Join(absTempDir, "key"), 0o600)

		// create client public key
		writeTestFile(t, s.tempDir, "key.pub",
			"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKeIs8mJqigBZ5y84J4COgnAJJ5bHPKy+lM2SliMXbYm radar@roark\n")
		os.Chmod(filepath.Join(absTempDir, "key.pub"), 0o600)

		// create authorized_keys
		writeTestFile(t, s.tempDir, "authorized_keys",
			"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKeIs8mJqigBZ5y84J4COgnAJJ5bHPKy+lM2SliMXbYm radar@roark\n")
		os.Chmod(authKeysPath, 0o600)

		// create known_hosts
		writeTestFile(t, s.tempDir, "known_hosts",
			fmt.Sprintf("[localhost]:%d ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTYAAAAIbmlzdHAyNTYAAABBBLWmlR+TyfvK9UoSDPe1eO3irvpUa6MtxCVHCaiDOi9XjQstxfRpM5tmVBotZ/Mkw2kJr/O0ylCWvzqexqsTiUQ=", s.port))
		os.Chmod(filepath.Join(absTempDir, "known_hosts"), 0o600)

		// create sshd_config
		writeTestFile(t, s.tempDir, "sshd_config",
			"AuthenticationMethods publickey\n"+
				"PubkeyAuthentication yes\n"+
				"PasswordAuthentication no\n"+
				"StrictModes no\n")
		os.Chmod(filepath.Join(absTempDir, "sshd_config"), 0o600)

		// create sshd.sh
		sshdPath, err := exec.LookPath("sshd")
		if err != nil {
			t.Fatalf("sshd not found: %v", err)
		}
		writeTestFile(t, s.tempDir, "sshd.sh",
			fmt.Sprintf("#!/bin/sh\nexec %s -p %d -f sshd_config -h \"%s\" -D -e -o AuthorizedKeysFile=\"%s\"",
				sshdPath, s.port, hostKeyPath, authKeysPath))
		os.Chmod(filepath.Join(absTempDir, "sshd.sh"), 0o755)

	case transportRaw:
		// nothing to init
	}
}

func (s *testServer) start(t *testing.T) {
	t.Helper()
	switch s.kind {
	case transportHTTP:
		go s.httpServer.Serve(s.httpListener)

	case transportRaw:
		portStr := fmt.Sprintf("--port=%d", s.port)
		s.process = exec.Command("git", "daemon", "--reuseaddr", "--base-path=.",
			"--export-all", "--enable=receive-pack", "--log-destination=stderr", portStr)
		s.process.Dir = s.tempDir
		s.process.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := s.process.Start(); err != nil {
			t.Fatalf("git daemon start failed: %v", err)
		}

	case transportSSH:
		s.process = exec.Command("./sshd.sh")
		s.process.Dir = s.tempDir
		s.process.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := s.process.Start(); err != nil {
			t.Fatalf("sshd start failed: %v", err)
		}
	}

	// wait for server to be ready by polling the port
	if s.kind != transportHTTP { // HTTP is ready immediately via Go's Serve
		addr := fmt.Sprintf("127.0.0.1:%d", s.port)
		for i := 0; i < 50; i++ {
			conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
			if err == nil {
				conn.Close()
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("server on port %d did not become ready", s.port)
	}
}

func (s *testServer) stop() {
	switch s.kind {
	case transportHTTP:
		s.httpServer.Close()
		s.httpListener.Close()
	case transportRaw, transportSSH:
		if s.process != nil && s.process.Process != nil {
			// kill the entire process group
			syscall.Kill(-s.process.Process.Pid, syscall.SIGKILL)
			s.process.Wait()
		}
	}
}

func (s *testServer) remoteURL(serverPath string) string {
	switch s.kind {
	case transportHTTP:
		return fmt.Sprintf("http://localhost:%d/server", s.port)
	case transportRaw:
		return fmt.Sprintf("git://localhost:%d/server", s.port)
	case transportSSH:
		return fmt.Sprintf("ssh://localhost:%d%s", s.port, serverPath)
	}
	return ""
}

func (s *testServer) sshConfigArg() string {
	absTempDir, _ := filepath.Abs(s.tempDir)
	privKeyPath := filepath.Join(absTempDir, "key")
	return fmt.Sprintf("core.sshCommand=ssh -o StrictHostKeyChecking=no -o IdentityFile=%s", privKeyPath)
}

// gitHTTPBackendPath finds the git-http-backend executable.
func gitHTTPBackendPath() string {
	if p, err := exec.LookPath("git-http-backend"); err == nil {
		return p
	}
	// try common locations
	for _, p := range []string{
		"/usr/lib/git-core/git-http-backend",
		"/usr/libexec/git-core/git-http-backend",
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "git-http-backend"
}

// runGit runs a git command and fails the test if exit code is non-zero.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %s failed: %v", strings.Join(args, " "), err)
	}
}

// runGitWithSSH runs a git command with SSH config and fails on non-zero exit.
func runGitWithSSH(t *testing.T, dir string, sshArg string, args ...string) {
	t.Helper()
	fullArgs := []string{"-c", sshArg}
	fullArgs = append(fullArgs, args...)
	cmd := exec.Command("git", fullArgs...)
	cmd.Dir = dir
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %s failed: %v", strings.Join(fullArgs, " "), err)
	}
}

// runGitMayFail runs a git command and ignores exit errors (only fails if process couldn't start).
func runGitMayFail(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		if _, ok := err.(*exec.ExitError); ok {
			return
		}
		t.Fatalf("git %s failed to start: %v", strings.Join(args, " "), err)
	}
}

// runGitWithSSHMayFail runs a git command with SSH config, ignoring exit errors.
func runGitWithSSHMayFail(t *testing.T, dir string, sshArg string, args ...string) {
	t.Helper()
	fullArgs := []string{"-c", sshArg}
	fullArgs = append(fullArgs, args...)
	cmd := exec.Command("git", fullArgs...)
	cmd.Dir = dir
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		if _, ok := err.(*exec.ExitError); ok {
			return
		}
		t.Fatalf("git %s failed to start: %v", strings.Join(fullArgs, " "), err)
	}
}

func writeTestFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatalf("write %s failed: %v", name, err)
	}
}

// initServerRepo creates a bare-ish server repo using git init and configures it.
func initServerRepo(t *testing.T, serverPath string) {
	t.Helper()
	runGit(t, ".", "init", serverPath)
	runGit(t, serverPath, "config", "user.name", "test")
	runGit(t, serverPath, "config", "user.email", "test@test")
}

// exportServerRepo creates the git-daemon-export-ok file.
func exportServerRepo(t *testing.T, serverPath string) {
	t.Helper()
	f, err := os.Create(filepath.Join(serverPath, ".git", "git-daemon-export-ok"))
	if err != nil {
		t.Fatalf("create export file failed: %v", err)
	}
	f.Close()
}

// --- Clone tests ---

func TestCloneHTTP(t *testing.T) {
	testClone(t, transportHTTP, 3031)
}

func TestCloneRaw(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("raw transport not supported on windows")
	}
	testClone(t, transportRaw, 3032)
}

func TestCloneSSH(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ssh transport not supported on windows")
	}
	testClone(t, transportSSH, 3033)
}

func testClone(t *testing.T, kind transportKind, port int) {
	tempDir := t.TempDir()

	server := newTestServer(kind, port, tempDir)
	server.init(t)
	server.start(t)
	defer server.stop()

	serverPath := filepath.Join(tempDir, "server")
	clientPath := filepath.Join(tempDir, "client")

	// init server repo with default branch "main"
	runGit(t, ".", "init", "-b", "main", serverPath)
	runGit(t, serverPath, "config", "user.name", "test")
	runGit(t, serverPath, "config", "user.email", "test@test")
	runGit(t, serverPath, "config", "uploadpack.allowfilter", "true")
	runGit(t, serverPath, "config", "uploadpack.allowAnySHA1InWant", "true")

	// make a commit
	writeTestFile(t, serverPath, "hello.txt", "hello, world!")
	runGit(t, serverPath, "add", "hello.txt")
	runGit(t, serverPath, "commit", "-m", "let there be light")

	// tag first commit
	runGit(t, serverPath, "tag", "-a", "v1", "-m", "first")

	// make another commit
	writeTestFile(t, serverPath, "goodbye.txt", "goodbye, world!")
	runGit(t, serverPath, "add", "goodbye.txt")
	runGit(t, serverPath, "commit", "-m", "add goodbye file")

	exportServerRepo(t, serverPath)

	remoteURL := server.remoteURL(serverPath)

	// shallow clone (--depth 1)
	if kind == transportSSH {
		runGitWithSSH(t, tempDir, server.sshConfigArg(), "clone", "--depth", "1", remoteURL, "client")
	} else {
		runGit(t, tempDir, "clone", "--depth", "1", remoteURL, "client")
	}

	// verify clone
	if _, err := os.Stat(filepath.Join(clientPath, "hello.txt")); err != nil {
		t.Fatalf("hello.txt not found after clone: %v", err)
	}

	// make a third commit on the server
	writeTestFile(t, serverPath, "extra.txt", "extra content")
	runGit(t, serverPath, "add", "extra.txt")
	runGit(t, serverPath, "commit", "-m", "add extra file")

	// pull --unshallow
	if kind == transportSSH {
		runGitWithSSH(t, clientPath, server.sshConfigArg(), "pull", "--unshallow")
	} else {
		runGit(t, clientPath, "pull", "--unshallow")
	}

	// verify unshallow pull
	if _, err := os.Stat(filepath.Join(clientPath, "extra.txt")); err != nil {
		t.Fatalf("extra.txt not found after unshallow pull: %v", err)
	}

	// clone with --shallow-since
	os.RemoveAll(clientPath)
	if kind == transportSSH {
		runGitWithSSH(t, tempDir, server.sshConfigArg(), "clone", "--shallow-since=2000-01-01", remoteURL, "client")
	} else {
		runGit(t, tempDir, "clone", "--shallow-since=2000-01-01", remoteURL, "client")
	}
	if _, err := os.Stat(filepath.Join(clientPath, "hello.txt")); err != nil {
		t.Fatalf("hello.txt not found after shallow-since clone: %v", err)
	}

	// clone with --shallow-exclude
	os.RemoveAll(clientPath)
	if kind == transportSSH {
		runGitWithSSH(t, tempDir, server.sshConfigArg(), "clone", "--shallow-exclude=v1", remoteURL, "client")
	} else {
		runGit(t, tempDir, "clone", "--shallow-exclude=v1", remoteURL, "client")
	}
	if _, err := os.Stat(filepath.Join(clientPath, "hello.txt")); err != nil {
		t.Fatalf("hello.txt not found after shallow-exclude clone: %v", err)
	}

	// clone with --filter=blob:none
	os.RemoveAll(clientPath)
	if kind == transportSSH {
		runGitWithSSHMayFail(t, tempDir, server.sshConfigArg(), "clone", "--filter=blob:none", remoteURL, "client")
	} else {
		runGitMayFail(t, tempDir, "clone", "--filter=blob:none", remoteURL, "client")
	}
	if _, err := os.Stat(filepath.Join(clientPath, "hello.txt")); err != nil {
		t.Fatalf("hello.txt not found after blob:none clone: %v", err)
	}

	// clone with --filter=tree:0
	os.RemoveAll(clientPath)
	if kind == transportSSH {
		runGitWithSSHMayFail(t, tempDir, server.sshConfigArg(), "clone", "--filter=tree:0", remoteURL, "client")
	} else {
		runGitMayFail(t, tempDir, "clone", "--filter=tree:0", remoteURL, "client")
	}
	if _, err := os.Stat(filepath.Join(clientPath, "goodbye.txt")); err != nil {
		t.Fatalf("goodbye.txt not found after tree:0 clone: %v", err)
	}
}

// --- Fetch tests ---

func TestFetchHTTP(t *testing.T) {
	testFetch(t, transportHTTP, 3022)
}

func TestFetchRaw(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("raw transport not supported on windows")
	}
	testFetch(t, transportRaw, 3023)
}

func TestFetchSSH(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ssh transport not supported on windows")
	}
	testFetch(t, transportSSH, 3024)
}

func testFetch(t *testing.T, kind transportKind, port int) {
	tempDir := t.TempDir()

	server := newTestServer(kind, port, tempDir)
	server.init(t)
	server.start(t)
	defer server.stop()

	serverPath := filepath.Join(tempDir, "server")
	clientPath := filepath.Join(tempDir, "client")

	// init server repo
	initServerRepo(t, serverPath)

	// copy Go source files into server dir to make a large repo
	copyGoFiles(t, ".", serverPath)

	runGit(t, serverPath, "add", ".")
	runGit(t, serverPath, "commit", "-m", "let there be light")

	exportServerRepo(t, serverPath)
	runGit(t, serverPath, "config", "uploadpack.allowrefinwant", "true")

	// get commit1 OID
	commit1 := gitRevParse(t, serverPath, "HEAD")

	// init client repo
	initServerRepo(t, clientPath)

	// add remote
	remoteURL := server.remoteURL(serverPath)
	runGit(t, clientPath, "remote", "add", "origin", remoteURL)
	runGit(t, clientPath, "config", "branch.master.remote", "origin")

	// pull
	if kind == transportSSH {
		runGitWithSSH(t, clientPath, server.sshConfigArg(), "pull", "origin", "master")
	} else {
		runGit(t, clientPath, "pull", "origin", "master")
	}

	// verify pull was successful
	clientHead := gitRevParse(t, clientPath, "HEAD")
	if clientHead != commit1 {
		t.Fatalf("client HEAD = %s, want %s", clientHead, commit1)
	}

	// make another commit on server
	writeTestFile(t, serverPath, "extra.txt", "extra content")
	runGit(t, serverPath, "add", "extra.txt")
	runGit(t, serverPath, "commit", "-m", "add extra file")
	commit2 := gitRevParse(t, serverPath, "HEAD")

	// fetch with want-ref
	if kind == transportSSH {
		runGitWithSSH(t, clientPath, server.sshConfigArg(), "fetch", "origin", "master")
	} else {
		runGit(t, clientPath, "fetch", "origin", "master")
	}

	// verify fetch was successful
	remoteMaster := gitRevParse(t, clientPath, "refs/remotes/origin/master")
	if remoteMaster != commit2 {
		t.Fatalf("origin/master = %s, want %s", remoteMaster, commit2)
	}
}

// --- Push tests ---

func TestPushHTTP(t *testing.T) {
	testPush(t, transportHTTP, 3028)
}

func TestPushRaw(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("raw transport not supported on windows")
	}
	testPush(t, transportRaw, 3029)
}

func TestPushSSH(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ssh transport not supported on windows")
	}
	testPush(t, transportSSH, 3030)
}

func testPush(t *testing.T, kind transportKind, port int) {
	tempDir := t.TempDir()

	server := newTestServer(kind, port, tempDir)
	server.init(t)
	server.start(t)
	defer server.stop()

	serverPath := filepath.Join(tempDir, "server")
	clientPath := filepath.Join(tempDir, "client")

	// init server repo
	initServerRepo(t, serverPath)
	runGit(t, serverPath, "config", "core.bare", "false")
	runGit(t, serverPath, "config", "receive.denycurrentbranch", "updateinstead")
	runGit(t, serverPath, "config", "http.receivepack", "true")
	exportServerRepo(t, serverPath)

	// init client repo
	initServerRepo(t, clientPath)

	// create a file and copy Go source files
	writeTestFile(t, clientPath, "hello.txt", "hello, world!")
	runGit(t, clientPath, "add", "hello.txt")
	copyGoFiles(t, ".", clientPath)
	runGit(t, clientPath, "add", ".")
	runGit(t, clientPath, "commit", "-m", "let there be light")

	// modify files so git sends delta objects
	modifyGoFiles(t, clientPath)
	runGit(t, clientPath, "add", ".")
	runGit(t, clientPath, "commit", "-m", "more stuff")
	commit2 := gitRevParse(t, clientPath, "HEAD")

	// add remote
	remoteURL := server.remoteURL(serverPath)
	runGit(t, clientPath, "remote", "add", "origin", remoteURL)
	runGit(t, clientPath, "config", "branch.master.remote", "origin")

	// push
	if kind == transportSSH {
		runGitWithSSH(t, clientPath, server.sshConfigArg(), "push", "origin", "master")
	} else {
		runGit(t, clientPath, "push", "origin", "master")
	}

	// verify push was successful
	serverHead := gitRevParse(t, serverPath, "HEAD")
	if serverHead != commit2 {
		t.Fatalf("server HEAD = %s, want %s", serverHead, commit2)
	}
	if _, err := os.Stat(filepath.Join(serverPath, "hello.txt")); err != nil {
		t.Fatalf("hello.txt not found on server after push: %v", err)
	}
}

// --- Helpers ---

// gitRevParse returns the OID for a given rev.
func gitRevParse(t *testing.T, dir, rev string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", rev)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse %s failed: %v", rev, err)
	}
	return strings.TrimSpace(string(out))
}

// copyGoFiles copies .go files from src into dst (for creating a large repo).
func copyGoFiles(t *testing.T, src, dst string) {
	t.Helper()
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatalf("read dir %s failed: %v", src, err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			t.Fatalf("read %s failed: %v", e.Name(), err)
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), data, 0644); err != nil {
			t.Fatalf("write %s failed: %v", e.Name(), err)
		}
	}
}

// modifyGoFiles prepends "// EDIT\n" to each .go file to trigger delta objects.
func modifyGoFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s failed: %v", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if err := os.WriteFile(path, append([]byte("// EDIT\n"), data...), 0644); err != nil {
			t.Fatalf("modify %s failed: %v", e.Name(), err)
		}
	}
}
