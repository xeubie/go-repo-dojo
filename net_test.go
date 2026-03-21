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
	"testing"
	"time"
)

// testServer is the interface for test transport servers.
type testServer interface {
	start(t *testing.T)
	stop()
	remoteURL(serverPath string) string
}

// runGitOnServer runs a git command, using SSH config if the server is an sshServer.
func runGitOnServer(t *testing.T, server testServer, dir string, args ...string) {
	t.Helper()
	if s, ok := server.(*sshServer); ok {
		runGitWithSSH(t, dir, s.sshConfigArg(), args...)
	} else {
		runGit(t, dir, args...)
	}
}


// --- HTTP server ---

type httpServer struct {
	port     int
	tempDir  string
	binPath  string
	listener net.Listener
	server   *http.Server
}

func newHTTPServer(port int, tempDir string, binPath string) *httpServer {
	return &httpServer{port: port, tempDir: tempDir, binPath: binPath}
}

func (s *httpServer) start(t *testing.T) {
	t.Helper()
	addr := fmt.Sprintf("127.0.0.1:%d", s.port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("listen on %s failed: %v", addr, err)
	}
	s.listener = ln

	absTempDir, err := filepath.Abs(s.tempDir)
	if err != nil {
		t.Fatalf("abs temp dir failed: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		pathTranslated := filepath.Join(absTempDir, r.URL.Path)
		handler := &cgi.Handler{
			Path: s.binPath,
			Args: []string{"http-backend"},
			Env: []string{
				"GIT_PROJECT_ROOT=" + absTempDir,
				"GIT_HTTP_EXPORT_ALL=1",
				"PATH_TRANSLATED=" + pathTranslated,
			},
		}
		handler.ServeHTTP(w, r)
	})

	s.server = &http.Server{Handler: mux}
	go s.server.Serve(s.listener)
}

func (s *httpServer) stop() {
	if s.server != nil {
		s.server.Close()
	}
	if s.listener != nil {
		s.listener.Close()
	}
}

func (s *httpServer) remoteURL(serverPath string) string {
	return fmt.Sprintf("http://localhost:%d/server", s.port)
}

// --- Raw git daemon server ---

type rawServer struct {
	port    int
	tempDir string
	process *exec.Cmd
}

func newRawServer(port int, tempDir string) *rawServer {
	return &rawServer{port: port, tempDir: tempDir}
}

func (s *rawServer) start(t *testing.T) {
	t.Helper()
	portStr := fmt.Sprintf("--port=%d", s.port)
	s.process = exec.Command("git", "daemon", "--reuseaddr", "--base-path=.",
		"--export-all", "--enable=receive-pack", "--log-destination=stderr", portStr)
	s.process.Dir = s.tempDir
	setProcGroup(s.process)
	if err := s.process.Start(); err != nil {
		t.Fatalf("git daemon start failed: %v", err)
	}
	waitForPort(t, s.port)
}

func (s *rawServer) stop() {
	if s.process != nil && s.process.Process != nil {
		killProcGroup(s.process)
		s.process.Wait()
	}
}

func (s *rawServer) remoteURL(serverPath string) string {
	return fmt.Sprintf("git://localhost:%d/server", s.port)
}

// --- SSH server ---

type sshServer struct {
	port    int
	tempDir string
	process *exec.Cmd
}

func newSSHServer(port int, tempDir string) *sshServer {
	return &sshServer{port: port, tempDir: tempDir}
}

func (s *sshServer) start(t *testing.T) {
	t.Helper()
	absTempDir, err := filepath.Abs(s.tempDir)
	if err != nil {
		t.Fatalf("abs temp dir failed: %v", err)
	}

	hostKeyPath := filepath.Join(absTempDir, "host_key")
	authKeysPath := filepath.Join(absTempDir, "authorized_keys")

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

	writeTestFile(t, s.tempDir, "key",
		"-----BEGIN OPENSSH PRIVATE KEY-----\n"+
			"b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW\n"+
			"QyNTUxOQAAACCniLPJiaooAWecvOCeAjoJwCSeWxzysvpTNkpYjF22JgAAAJA+7hikPu4Y\n"+
			"pAAAAAtzc2gtZWQyNTUxOQAAACCniLPJiaooAWecvOCeAjoJwCSeWxzysvpTNkpYjF22Jg\n"+
			"AAAEDVlopOMnKt/7by/IA8VZvQXUS/O6VLkixOqnnahUdPCKeIs8mJqigBZ5y84J4COgnA\n"+
			"JJ5bHPKy+lM2SliMXbYmAAAAC3JhZGFyQHJvYXJrAQI=\n"+
			"-----END OPENSSH PRIVATE KEY-----\n")
	os.Chmod(filepath.Join(absTempDir, "key"), 0o600)

	writeTestFile(t, s.tempDir, "key.pub",
		"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKeIs8mJqigBZ5y84J4COgnAJJ5bHPKy+lM2SliMXbYm radar@roark\n")
	os.Chmod(filepath.Join(absTempDir, "key.pub"), 0o600)

	writeTestFile(t, s.tempDir, "authorized_keys",
		"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKeIs8mJqigBZ5y84J4COgnAJJ5bHPKy+lM2SliMXbYm radar@roark\n")
	os.Chmod(authKeysPath, 0o600)

	writeTestFile(t, s.tempDir, "known_hosts",
		fmt.Sprintf("[localhost]:%d ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTYAAAAIbmlzdHAyNTYAAABBBLWmlR+TyfvK9UoSDPe1eO3irvpUa6MtxCVHCaiDOi9XjQstxfRpM5tmVBotZ/Mkw2kJr/O0ylCWvzqexqsTiUQ=", s.port))
	os.Chmod(filepath.Join(absTempDir, "known_hosts"), 0o600)

	writeTestFile(t, s.tempDir, "sshd_config",
		"AuthenticationMethods publickey\n"+
			"PubkeyAuthentication yes\n"+
			"PasswordAuthentication no\n"+
			"StrictModes no\n")
	os.Chmod(filepath.Join(absTempDir, "sshd_config"), 0o600)

	sshdPath, err := exec.LookPath("sshd")
	if err != nil {
		t.Fatalf("sshd not found: %v", err)
	}
	writeTestFile(t, s.tempDir, "sshd.sh",
		fmt.Sprintf("#!/bin/sh\nexec %s -p %d -f sshd_config -h \"%s\" -D -e -o AuthorizedKeysFile=\"%s\"",
			sshdPath, s.port, hostKeyPath, authKeysPath))
	os.Chmod(filepath.Join(absTempDir, "sshd.sh"), 0o755)

	s.process = exec.Command("./sshd.sh")
	s.process.Dir = s.tempDir
	setProcGroup(s.process)
	if err := s.process.Start(); err != nil {
		t.Fatalf("sshd start failed: %v", err)
	}
	waitForPort(t, s.port)
}

func (s *sshServer) stop() {
	if s.process != nil && s.process.Process != nil {
		killProcGroup(s.process)
		s.process.Wait()
	}
}

func (s *sshServer) remoteURL(serverPath string) string {
	return fmt.Sprintf("ssh://localhost:%d%s", s.port, serverPath)
}

func (s *sshServer) sshConfigArg() string {
	absTempDir, _ := filepath.Abs(s.tempDir)
	privKeyPath := filepath.Join(absTempDir, "key")
	return fmt.Sprintf("core.sshCommand=ssh -o StrictHostKeyChecking=no -o IdentityFile=%s", privKeyPath)
}

func buildBinary(t *testing.T, tempDir string) string {
	t.Helper()
	binPath := filepath.Join(tempDir, "repomofo")
	cmd := exec.Command("go", "build", "-o", binPath, "./cmd/repomofo")
	cmd.Dir = "."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build repomofo binary failed: %v\n%s", err, out)
	}
	return binPath
}

// --- Helpers ---

func waitForPort(t *testing.T, port int) {
	t.Helper()
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	for i := 0; i < 50; i++ {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("server on port %d did not become ready", port)
}

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


func writeTestFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatalf("write %s failed: %v", name, err)
	}
}

func initServerRepo(t *testing.T, serverPath string) {
	t.Helper()
	runGit(t, ".", "init", serverPath)
	runGit(t, serverPath, "config", "user.name", "test")
	runGit(t, serverPath, "config", "user.email", "test@test")
}

func exportServerRepo(t *testing.T, serverPath string) {
	t.Helper()
	f, err := os.Create(filepath.Join(serverPath, ".git", "git-daemon-export-ok"))
	if err != nil {
		t.Fatalf("create export file failed: %v", err)
	}
	f.Close()
}

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

// --- Clone tests ---

func TestCloneHTTP(t *testing.T) {
	tempDir := t.TempDir()
	binPath := buildBinary(t, tempDir)
	testClone(t, newHTTPServer(3031, tempDir, binPath), tempDir, nil)
}

func TestCloneRaw(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("raw transport not supported on windows")
	}
	tempDir := t.TempDir()
	testClone(t, newRawServer(3032, tempDir), tempDir, nil)
}

func TestCloneSSH(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ssh transport not supported on windows")
	}
	tempDir := t.TempDir()
	binPath := buildBinary(t, tempDir)
	extraArgs := []string{"--upload-pack", binPath + " upload-pack"}
	testClone(t, newSSHServer(3033, tempDir), tempDir, extraArgs)
}

func testClone(t *testing.T, server testServer, tempDir string, extraArgs []string) {
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
	runGitOnServer(t, server, tempDir, append([]string{"clone"}, append(extraArgs, "--depth", "1", remoteURL, "client")...)...)

	// verify clone
	if _, err := os.Stat(filepath.Join(clientPath, "hello.txt")); err != nil {
		t.Fatalf("hello.txt not found after clone: %v", err)
	}

	// make a third commit on the server
	writeTestFile(t, serverPath, "extra.txt", "extra content")
	runGit(t, serverPath, "add", "extra.txt")
	runGit(t, serverPath, "commit", "-m", "add extra file")

	// pull --unshallow
	runGitOnServer(t, server, clientPath, append([]string{"pull"}, append(extraArgs, "--unshallow")...)...)

	// verify unshallow pull
	if _, err := os.Stat(filepath.Join(clientPath, "extra.txt")); err != nil {
		t.Fatalf("extra.txt not found after unshallow pull: %v", err)
	}

	// clone with --shallow-since
	os.RemoveAll(clientPath)
	runGitOnServer(t, server, tempDir, append([]string{"clone"}, append(extraArgs, "--shallow-since=2000-01-01", remoteURL, "client")...)...)
	if _, err := os.Stat(filepath.Join(clientPath, "hello.txt")); err != nil {
		t.Fatalf("hello.txt not found after shallow-since clone: %v", err)
	}

	// clone with --shallow-exclude
	os.RemoveAll(clientPath)
	runGitOnServer(t, server, tempDir, append([]string{"clone"}, append(extraArgs, "--shallow-exclude=v1", remoteURL, "client")...)...)
	if _, err := os.Stat(filepath.Join(clientPath, "hello.txt")); err != nil {
		t.Fatalf("hello.txt not found after shallow-exclude clone: %v", err)
	}

	// clone with --filter=blob:none
	os.RemoveAll(clientPath)
	runGitOnServer(t, server, tempDir, append([]string{"clone"}, append(extraArgs, "--filter=blob:none", remoteURL, "client")...)...)
	if _, err := os.Stat(filepath.Join(clientPath, "hello.txt")); err != nil {
		t.Fatalf("hello.txt not found after blob:none clone: %v", err)
	}

	// clone with --filter=tree:0
	os.RemoveAll(clientPath)
	runGitOnServer(t, server, tempDir, append([]string{"clone"}, append(extraArgs, "--filter=tree:0", remoteURL, "client")...)...)
	if _, err := os.Stat(filepath.Join(clientPath, "goodbye.txt")); err != nil {
		t.Fatalf("goodbye.txt not found after tree:0 clone: %v", err)
	}
}

// --- Fetch tests ---

func TestFetchHTTP(t *testing.T) {
	tempDir := t.TempDir()
	binPath := buildBinary(t, tempDir)
	testFetch(t, newHTTPServer(3022, tempDir, binPath), tempDir, nil)
}

func TestFetchRaw(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("raw transport not supported on windows")
	}
	tempDir := t.TempDir()
	testFetch(t, newRawServer(3023, tempDir), tempDir, nil)
}

func TestFetchSSH(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ssh transport not supported on windows")
	}
	tempDir := t.TempDir()
	binPath := buildBinary(t, tempDir)
	extraArgs := []string{"--upload-pack", binPath + " upload-pack"}
	testFetch(t, newSSHServer(3024, tempDir), tempDir, extraArgs)
}

func testFetch(t *testing.T, server testServer, tempDir string, extraArgs []string) {
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
	runGitOnServer(t, server, clientPath, append([]string{"pull"}, append(extraArgs, "origin", "master")...)...)

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
	runGitOnServer(t, server, clientPath, append([]string{"fetch"}, append(extraArgs, "origin", "master")...)...)

	// verify fetch was successful
	remoteMaster := gitRevParse(t, clientPath, "refs/remotes/origin/master")
	if remoteMaster != commit2 {
		t.Fatalf("origin/master = %s, want %s", remoteMaster, commit2)
	}
}

// --- Push tests ---

func TestPushHTTP(t *testing.T) {
	tempDir := t.TempDir()
	binPath := buildBinary(t, tempDir)
	testPush(t, newHTTPServer(3028, tempDir, binPath), tempDir, nil)
}

func TestPushRaw(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("raw transport not supported on windows")
	}
	tempDir := t.TempDir()
	testPush(t, newRawServer(3029, tempDir), tempDir, nil)
}

func TestPushSSH(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ssh transport not supported on windows")
	}
	tempDir := t.TempDir()
	binPath := buildBinary(t, tempDir)
	extraArgs := []string{"--receive-pack", binPath + " receive-pack"}
	testPush(t, newSSHServer(3030, tempDir), tempDir, extraArgs)
}

func testPush(t *testing.T, server testServer, tempDir string, extraArgs []string) {
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
	runGitOnServer(t, server, clientPath, append([]string{"push"}, append(extraArgs, "origin", "master")...)...)

	// verify push was successful
	serverHead := gitRevParse(t, serverPath, "HEAD")
	if serverHead != commit2 {
		t.Fatalf("server HEAD = %s, want %s", serverHead, commit2)
	}
	if _, err := os.Stat(filepath.Join(serverPath, "hello.txt")); err != nil {
		t.Fatalf("hello.txt not found on server after push: %v", err)
	}
}
