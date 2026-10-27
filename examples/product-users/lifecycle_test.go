package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
)

// This provider serves real HTTP discovery only. No login/token proof is mocked
// or claimed here: the executable test exercises startup and process shutdown.
func lifecycleIssuer(t *testing.T) string {
	t.Helper()
	var issuer *httptest.Server
	issuer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"issuer": issuer.URL, "authorization_endpoint": issuer.URL + "/authorize", "token_endpoint": issuer.URL + "/token", "jwks_uri": issuer.URL + "/jwks", "id_token_signing_alg_values_supported": []string{"RS256"}})
	}))
	t.Cleanup(issuer.Close)
	return issuer.URL
}
func lifecycleConfig(t *testing.T, dir, issuer string) (string, config) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	c := config{Mode: "standalone", Listen: address, PublicURL: "http://" + address, OIDCIssuer: issuer, ClientID: "lifecycle-client", ProductID: "notes", InstanceID: "default", Database: filepath.Join(dir, "product.json")}
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path, c
}
func startLifecycleProcess(t *testing.T, binary, path string) (*exec.Cmd, *bytes.Buffer, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	command := exec.CommandContext(ctx, binary)
	command.Env = append(os.Environ(), "PRODUCT_CONFIG="+path)
	output := new(bytes.Buffer)
	command.Stdout = output
	command.Stderr = output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait(); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return command, output, done
}
func awaitLifecycleReady(t *testing.T, origin string, done <-chan error) {
	t.Helper()
	client := http.Client{Timeout: 200 * time.Millisecond}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			t.Fatalf("starter exited before ready: %v", err)
		default:
		}
		if response, err := client.Get(origin); err == nil {
			response.Body.Close()
			if response.StatusCode == 200 {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("starter did not become ready")
}
func awaitLifecycleExit(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("starter did not stop")
		return nil
	}
}
func TestStarterExecutableLifecycle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX process signals")
	}
	binary := filepath.Join(t.TempDir(), "product")
	if output, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	issuer := lifecycleIssuer(t)
	for _, signal := range []os.Signal{syscall.SIGTERM, os.Interrupt} {
		t.Run(fmt.Sprint(signal), func(t *testing.T) {
			path, c := lifecycleConfig(t, t.TempDir(), issuer)
			command, _, done := startLifecycleProcess(t, binary, path)
			awaitLifecycleReady(t, c.PublicURL, done)
			if err := command.Process.Signal(signal); err != nil {
				t.Fatal(err)
			}
			stopErr := awaitLifecycleExit(t, done)
			// Try the identical database without repairing/removing its lock.
			restarted, _, again := startLifecycleProcess(t, binary, path)
			awaitLifecycleReady(t, c.PublicURL, again)
			if err := restarted.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			if err := awaitLifecycleExit(t, again); err != nil {
				t.Fatalf("restart stop: %v", err)
			}
			if stopErr != nil {
				t.Fatalf("normal stop returned error: %v", stopErr)
			}
			if _, err := os.Stat(c.Database + ".lock"); !os.IsNotExist(err) {
				t.Fatal("normal shutdown left the database locked")
			}
		})
	}
	t.Run("initialization failure releases lock", func(t *testing.T) {
		dir := t.TempDir()
		path, c := lifecycleConfig(t, dir, issuer+"/unavailable")
		_, _, done := startLifecycleProcess(t, binary, path)
		if err := awaitLifecycleExit(t, done); err == nil {
			t.Fatal("unavailable discovery unexpectedly started")
		}
		c.OIDCIssuer = issuer
		raw, _ := json.Marshal(c)
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		retry, _, again := startLifecycleProcess(t, binary, path)
		awaitLifecycleReady(t, c.PublicURL, again)
		if err := retry.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		if err := awaitLifecycleExit(t, again); err != nil {
			t.Fatalf("corrected retry stop: %v", err)
		}
		if _, err := os.Stat(c.Database + ".lock"); !os.IsNotExist(err) {
			t.Fatal("corrected retry left the database locked")
		}
	})
}
