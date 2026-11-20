package httpapi

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stearsc/web-ssh/backend/internal/auth"
	"github.com/stearsc/web-ssh/backend/internal/config"
	secret "github.com/stearsc/web-ssh/backend/internal/crypto"
	"github.com/stearsc/web-ssh/backend/internal/privatekey"
	servers "github.com/stearsc/web-ssh/backend/internal/server"
	"github.com/stearsc/web-ssh/backend/internal/session"
	"github.com/stearsc/web-ssh/backend/internal/testutil"
	browser "github.com/stearsc/web-ssh/backend/internal/websocket"
	"golang.org/x/crypto/ssh"
)

// 只有浏览器专项显式启用时运行；Go/SQLite/SSH/WS 都是真实路径，不启动生产服务。
func TestStage6KBrowserFixture(t *testing.T) {
	if os.Getenv("WEB_SSH_6K_BROWSER_FIXTURE") != "1" {
		t.Skip("浏览器集成专用夹具未启用")
	}
	output := os.Getenv("WEB_SSH_6K_FIXTURE_DIR")
	origin := os.Getenv("WEB_SSH_6K_ORIGIN")
	if output == "" || origin == "" {
		t.Fatal("缺少浏览器夹具参数")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	keys := filepath.Join(dir, "keys")
	if err = os.Mkdir(keys, 0700); err != nil {
		t.Fatal(err)
	}
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(key)
	block, err := ssh.MarshalPrivateKeyWithPassphrase(key, "", []byte("browser-test-passphrase"))
	if err != nil {
		t.Fatal(err)
	}
	upload := filepath.Join(dir, "browser-upload.key")
	if err = os.WriteFile(upload, pem.EncodeToMemory(block), 0600); err != nil {
		t.Fatal(err)
	}
	remote := testutil.StartTerminalSSH(t, testutil.TerminalOptions{AuthorizedKey: signer.PublicKey()})
	db := testutil.DB(t)
	cfg := config.Defaults()
	cfg.Auth.CookieSecure = false
	cfg.Server.PublicOrigin = origin
	cfg.Auth.LoginRateLimit = 100
	admin, err := auth.New(t.Context(), db, cfg.Auth, "browser-admin-password")
	if err != nil {
		t.Fatal(err)
	}
	cipher, _ := secret.New(make([]byte, 32))
	managed, err := servers.New(t.Context(), db, cipher, cfg, servers.WithPrivateKeyStore(privatekey.NewStore(db, keys, time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sessions := session.New(ctx, db, managed, cfg, logger)
	sockets := browser.New(cfg, logger)
	admin.OnRevoke(func(id string) { managed.Revoke(id); sessions.Revoke(id); sockets.Revoke(id) })
	management := &Management{Auth: admin, Servers: managed, Sessions: sessions, Websocket: sockets, Config: cfg, Logger: logger}
	handler := management.Handler(NewHandler(fstest.MapFS{"index.html": {Data: []byte("browser fixture")}}, func() bool { return true }, logger), func() bool { return true })
	httpServer := httptest.NewUnstartedServer(handler)
	httpServer.Config.ConnContext = browser.ConnContext
	httpServer.Start()
	defer func() {
		cancel()
		sessions.Stop()
		sockets.Stop(time.Now().Add(2 * time.Second))
		httpServer.Close()
		<-sessions.Done()
		wait, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		sockets.Wait(wait)
	}()
	data, _ := json.Marshal(struct {
		URL    string `json:"url"`
		Upload string `json:"upload"`
		Port   int    `json:"port"`
	}{httpServer.URL, upload, remote.Port()})
	if err = os.WriteFile(filepath.Join(output, "fixture.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if _, err = os.Stat(filepath.Join(output, "stop")); err == nil {
				return
			}
		case <-t.Context().Done():
			return
		}
	}
}
