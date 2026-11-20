package session

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"github.com/stearsc/web-ssh/backend/internal/config"
	"github.com/stearsc/web-ssh/backend/internal/fault"
	"github.com/stearsc/web-ssh/backend/internal/testutil"
	"golang.org/x/crypto/ssh"
)

// 这里使用真实 OpenSSH 子系统和真实 SSH channel；缺少程序的平台明确跳过该组，
// 不把 pkg/sftp 测试服务端的行为当作 OpenSSH 能力证明。
func openSSHFileServer(t *testing.T, arguments ...string) func(ssh.NewChannel, <-chan struct{}) {
	t.Helper()
	program := "/usr/libexec/sftp-server"
	if _, err := os.Stat(program); err != nil {
		t.Skip("本机未提供 OpenSSH sftp-server")
	}
	return func(incoming ssh.NewChannel, done <-chan struct{}) {
		ch, requests, err := incoming.Accept()
		if err != nil {
			return
		}
		defer ch.Close()
		for r := range requests {
			if r.Type != "subsystem" {
				_ = r.Reply(false, nil)
				continue
			}
			_ = r.Reply(true, nil)
			cmd := exec.Command(program, arguments...)
			cmd.Stdin = ch
			cmd.Stdout = ch
			cmd.Stderr = io.Discard
			if err = cmd.Start(); err != nil {
				t.Error(err)
				return
			}
			exited := make(chan struct{})
			watcherDone := make(chan struct{})
			go func() {
				defer close(watcherDone)
				select {
				case <-done:
					_ = cmd.Process.Kill()
				case <-exited:
				}
			}()
			_ = cmd.Wait()
			close(exited)
			<-watcherDone
			return
		}
	}
}
func uploadSession(t *testing.T, configure ...func(*config.Config)) (*Session, string) {
	t.Helper()
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var configuration func(*config.Config)
	if len(configure) > 0 {
		configuration = configure[0]
	}
	m, login, id, _ := setupWith(t, testutil.TerminalOptions{FileChannel: openSSHFileServer(t)}, configuration)
	return createSession(t, m, login, id), directory
}
func source(text string) FileSource { return FileSource{Reader: strings.NewReader(text)} }
func assertNoTemporary(t *testing.T, directory string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(directory, ".web-ssh-upload-*"))
	if err != nil || len(matches) != 0 {
		t.Fatal("临时文件残留", matches, err)
	}
}

func TestSFTPUploadRealOpenSSH(t *testing.T) {
	s, directory := uploadSession(t)
	check, err := s.CheckFile(t.Context(), directory, "file.txt")
	if err != nil || check.Exists {
		t.Fatal(check, err)
	}
	result, err := s.UploadFile(t.Context(), directory, "file.txt", false, source("first\n多行内容\n"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(result.Path)
	if err != nil || string(got) != "first\n多行内容\n" || int64(len(got)) != result.Size {
		t.Fatal(result, string(got), err)
	}
	check, err = s.CheckFile(t.Context(), directory, "file.txt")
	if err != nil || !check.Exists {
		t.Fatal(check, err)
	}
	if _, err = s.UploadFile(t.Context(), directory, "file.txt", false, source("must-not-replace")); err != fault.Error("SFTP_FILE_EXISTS") {
		t.Fatal(err)
	}
	if _, err = s.UploadFile(t.Context(), directory, "file.txt", true, source("new-content")); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(result.Path)
	if string(got) != "new-content" {
		t.Fatal(string(got))
	}
	if _, err = s.UploadFile(t.Context(), directory, "file.txt", true, FileSource{Reader: strings.NewReader("bad"), Finish: func() error { return fault.Error("INVALID_REQUEST") }}); err != fault.Error("INVALID_REQUEST") {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(result.Path)
	if string(got) != "new-content" {
		t.Fatal("失败覆盖破坏原文件", string(got))
	}
	assertNoTemporary(t, directory)
	if _, err = s.terminal.Write([]byte("terminal-during-sftp")); err != nil {
		t.Fatal(err)
	}
	waitBytes(t, s, []byte("terminal-during-sftp"))
}

func TestSFTPPathValidation(t *testing.T) {
	s, directory := uploadSession(t)
	if err := os.Mkdir(filepath.Join(directory, "folder"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(directory, filepath.Join(directory, "alias")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(directory, "missing"), filepath.Join(directory, "link")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", ".", "..", "../escape", "a/b", "a\\b", "bad\x00name", "folder", "link"} {
		t.Run(name, func(t *testing.T) {
			if _, err := s.CheckFile(t.Context(), directory, name); err != fault.Error("SFTP_INVALID_PATH") {
				t.Fatal(err)
			}
		})
	}
	for _, dir := range []string{"relative", directory + "/../x", directory + "/alias", directory + "/alias/sub"} {
		if _, err := s.CheckFile(t.Context(), dir, "f"); err != fault.Error("SFTP_INVALID_PATH") {
			t.Fatal(dir, err)
		}
	}
	if _, err := s.CheckFile(t.Context(), directory+"/absent", "f"); err != fault.Error("SFTP_DIRECTORY_UNAVAILABLE") {
		t.Fatal(err)
	}
	assertNoTemporary(t, directory)
}

type failReader struct{}

func (failReader) Read([]byte) (int, error) { return 0, errors.New("测试读取错误") }
func TestSFTPReadFailureAndCancel(t *testing.T) {
	s, directory := uploadSession(t)
	target := filepath.Join(directory, "old")
	if err := os.WriteFile(target, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UploadFile(t.Context(), directory, "old", true, FileSource{Reader: io.MultiReader(strings.NewReader("partial"), failReader{})}); err != fault.Error("SFTP_READ_FAILED") {
		t.Fatal(err)
	}
	reader, writer := io.Pipe()
	defer writer.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	entered := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		_, err := s.UploadFile(ctx, directory, "old", true, FileSource{Reader: reader, Interrupt: func() { reader.CloseWithError(context.Canceled) }})
		result <- err
	}()
	go func() {
		_, err := io.WriteString(writer, "part")
		if err != nil {
			t.Error(err)
		}
		close(entered)
	}()
	<-entered
	if _, err := s.terminal.Write([]byte("shell-while-uploading")); err != nil {
		t.Fatal(err)
	}
	waitBytes(t, s, []byte("shell-while-uploading"))
	cancel()
	if err := awaitFileResult(t, result); err != fault.Error("SFTP_CANCELED") {
		t.Fatal(err)
	}
	assertNoTemporary(t, directory)
	got, _ := os.ReadFile(target)
	if string(got) != "old" {
		t.Fatal(string(got))
	}
	if s.View().Status != "DISCONNECTED" {
		t.Fatal(s.View())
	}
}

type repeatedReader struct{}

func (repeatedReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }
func TestSFTPActual512MiBBoundary(t *testing.T) {
	if testing.Short() {
		t.Skip("真实 512 MiB 边界传输")
	}
	s, directory := uploadSession(t)
	for _, extra := range []int64{0, 1} {
		t.Run(string(rune('0'+extra)), func(t *testing.T) {
			name := "large"
			if extra == 1 {
				name = "over"
			}
			result, err := s.UploadFile(t.Context(), directory, name, false, FileSource{Reader: io.LimitReader(repeatedReader{}, MaxUploadSize+extra)})
			if extra == 1 {
				if err != fault.Error("SFTP_TOO_LARGE") {
					t.Fatal(err)
				}
				if _, err = os.Stat(filepath.Join(directory, name)); !os.IsNotExist(err) {
					t.Fatal(err)
				}
			} else {
				if err != nil || result.Size != MaxUploadSize {
					t.Fatal(result, err)
				}
				file, err := os.Open(result.Path)
				if err != nil {
					t.Fatal(err)
				}
				actual := sha256.New()
				_, err = io.Copy(actual, file)
				file.Close()
				if err != nil {
					t.Fatal(err)
				}
				expected := sha256.New()
				_, err = io.Copy(expected, io.LimitReader(repeatedReader{}, MaxUploadSize))
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(actual.Sum(nil), expected.Sum(nil)) {
					t.Fatal("大文件哈希不一致")
				}
			}
			assertNoTemporary(t, directory)
		})
	}
}

func TestSFTPErrorClasses(t *testing.T) {
	for _, item := range []struct {
		status uint32
		want   fault.Error
	}{{3, "SFTP_PERMISSION_DENIED"}, {14, "SFTP_NO_SPACE"}, {15, "SFTP_NO_SPACE"}, {7, "SFTP_UNAVAILABLE"}} {
		if got := fileError(&sftp.StatusError{Code: item.status}); got != item.want {
			t.Fatal(got, item.want)
		}
	}
}

func TestSFTPFailureReceiptExpires(t *testing.T) {
	m, login, id, _ := setup(t)
	s := createSession(t, m, login, id)
	s.recordFileFailure()
	s.Close("ssh_error")
	<-s.Done()
	if _, err := m.FindForFiles(t.Context(), s.id); err != fault.Error("SFTP_CANCEL_FAILED") {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.fileFailures[s.id] = time.Now().Add(-time.Second)
	m.mu.Unlock()
	m.Sweep()
	if _, err := m.FindForFiles(t.Context(), s.id); err != fault.Error("SESSION_NOT_FOUND") {
		t.Fatal(err)
	}
}

type failingFilePut struct {
	delegate sftp.FileWriter
	denied   bool
}

func (p failingFilePut) Filewrite(r *sftp.Request) (io.WriterAt, error) {
	if p.denied {
		return nil, sftp.ErrSSHFxPermissionDenied
	}
	writer, err := p.delegate.Filewrite(r)
	if err != nil {
		return nil, err
	}
	return noSpaceWriter{writer}, nil
}

type noSpaceWriter struct{ io.WriterAt }

func (w noSpaceWriter) WriteAt(p []byte, offset int64) (int, error) {
	if offset > 0 {
		return 0, syscall.ENOSPC
	}
	return w.WriterAt.WriteAt(p, offset)
}
func (w noSpaceWriter) Close() error {
	if closer, ok := w.WriterAt.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

type rejectCommit struct{ sftp.FileCmder }

func (r rejectCommit) PosixRename(*sftp.Request) error { return sftp.ErrSSHFxPermissionDenied }

func TestSFTPProtocolWriteAndCommitFailures(t *testing.T) {
	for _, mode := range []string{"permission", "no-space", "commit"} {
		t.Run(mode, func(t *testing.T) {
			handlers, _ := permissionMemoryHandlers()
			if mode == "commit" {
				handlers.FileCmd = rejectCommit{handlers.FileCmd}
			} else {
				handlers.FilePut = failingFilePut{handlers.FilePut, mode == "permission"}
			}
			m, login, id, _ := setupWith(t, testutil.TerminalOptions{FileChannel: func(incoming ssh.NewChannel, _ <-chan struct{}) {
				ch, requests, err := incoming.Accept()
				if err != nil {
					return
				}
				defer ch.Close()
				r := <-requests
				if r == nil {
					return
				}
				_ = r.Reply(true, nil)
				srv := sftp.NewRequestServer(ch, handlers)
				_ = srv.Serve()
				_ = srv.Close()
			}}, nil)
			s := createSession(t, m, login, id)
			if mode == "commit" {
				if err := s.withFileClient(t.Context(), func(_ context.Context, c *sftp.Client, _ func()) error {
					f, err := c.Create("/old")
					if err != nil {
						return err
					}
					_, err = io.WriteString(f, "ORIGINAL")
					closeErr := f.Close()
					if err != nil {
						return err
					}
					return closeErr
				}); err != nil {
					t.Fatal(err)
				}
			}
			name := "new"
			if mode == "commit" {
				name = "old"
			}
			_, err := s.UploadFile(t.Context(), "/", name, mode == "commit", FileSource{Reader: bytes.NewReader(bytes.Repeat([]byte{1}, 128<<10))})
			want := fault.Error("SFTP_PERMISSION_DENIED")
			if mode == "no-space" {
				want = "SFTP_FAILED"
			}
			if err != want {
				t.Fatal(err, want)
			}
			if err = s.withFileClient(t.Context(), func(_ context.Context, c *sftp.Client, _ func()) error {
				list, e := c.ReadDir("/")
				if e != nil {
					return e
				}
				for _, file := range list {
					if strings.HasPrefix(file.Name(), ".web-ssh-upload-") {
						t.Error("临时文件未清理")
					}
				}
				if mode == "commit" {
					file, e := c.Open("/old")
					if e != nil {
						return e
					}
					data, e := io.ReadAll(file)
					_ = file.Close()
					if e != nil {
						return e
					}
					if string(data) != "ORIGINAL" {
						t.Error("提交失败破坏原文件")
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSFTPUnsupportedAtomicReplacement(t *testing.T) {
	if err := sftp.SetSFTPExtensions("hardlink@openssh.com"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := sftp.SetSFTPExtensions("hardlink@openssh.com", "posix-rename@openssh.com", "statvfs@openssh.com"); err != nil {
			t.Fatal(err)
		}
	}()
	// 子测试退出后 SSH 服务和所有 Session 均已回收，再恢复全局测试对端扩展配置。
	t.Run("no-posix-rename", func(t *testing.T) {
		directory, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(directory, "old")
		if err = os.WriteFile(target, []byte("ORIGINAL"), 0600); err != nil {
			t.Fatal(err)
		}
		m, login, id, _ := setupWith(t, testutil.TerminalOptions{FileChannel: fileServer(t, directory)}, nil)
		s := createSession(t, m, login, id)
		if _, err = s.UploadFile(t.Context(), directory, "old", true, source("replacement")); err != fault.Error("SFTP_ATOMIC_UNAVAILABLE") {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(target)
		if string(data) != "ORIGINAL" {
			t.Fatal(string(data))
		}
		assertNoTemporary(t, directory)
	})
}

func TestSFTPActiveTransferSessionCloseAndTransportLoss(t *testing.T) {
	for _, mode := range []string{"session-close", "transport-loss"} {
		t.Run(mode, func(t *testing.T) {
			s, directory := uploadSession(t)
			reader, writer := io.Pipe()
			defer writer.Close()
			consumed := make(chan struct{})
			result := make(chan error, 1)
			go func() {
				_, err := s.UploadFile(t.Context(), directory, "unfinished", false, FileSource{Reader: reader, Interrupt: func() { _ = reader.CloseWithError(context.Canceled) }})
				result <- err
			}()
			go func() {
				_, err := io.WriteString(writer, "partial-content")
				if err != nil {
					t.Error(err)
				}
				close(consumed)
			}()
			<-consumed
			if mode == "session-close" {
				s.Close("user_closed")
			} else {
				s.terminal.Close()
			}
			if err := awaitFileResult(t, result); err != fault.Error("SFTP_CANCELED") && err != fault.Error("SFTP_UNAVAILABLE") {
				t.Fatal(err)
			}
			select {
			case <-s.Done():
			case <-time.After(3 * time.Second):
				t.Fatal("上传未随 Session 回收")
			}
			if _, err := os.Stat(filepath.Join(directory, "unfinished")); !os.IsNotExist(err) {
				t.Fatal("失败传输提交了最终文件", err)
			}
			// transport 已断时无法保证发出 Remove；此处只检查没有提交最终文件，残留清理限制需如实报告。
		})
	}
}

func TestSFTPTransferProgressResetsIdleBudget(t *testing.T) {
	s, directory := uploadSession(t, func(c *config.Config) { c.Server.RequestTimeout = 1 })
	reader, writer := io.Pipe()
	producer := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for range 10 {
			<-ticker.C
			if _, err := writer.Write([]byte("progress")); err != nil {
				producer <- err
				return
			}
		}
		producer <- writer.Close()
	}()
	result, err := s.UploadFile(t.Context(), directory, "slow-progress", false, FileSource{Reader: reader, Interrupt: func() { _ = reader.CloseWithError(context.Canceled) }})
	if err != nil {
		t.Fatal(err)
	}
	if err = <-producer; err != nil {
		t.Fatal(err)
	}
	if result.Size != 80 || s.View().Status != "DISCONNECTED" {
		t.Fatal(result, s.View())
	}
	data, err := os.ReadFile(result.Path)
	if err != nil || string(data) != strings.Repeat("progress", 10) {
		t.Fatal(string(data), err)
	}
	assertNoTemporary(t, directory)
}

func TestSFTPSubsystemUnavailableKeepsTerminal(t *testing.T) {
	m, login, id, _ := setupWith(t, testutil.TerminalOptions{FileChannel: func(incoming ssh.NewChannel, _ <-chan struct{}) {
		ch, requests, err := incoming.Accept()
		if err != nil {
			return
		}
		defer ch.Close()
		for r := range requests {
			_ = r.Reply(false, nil)
		}
	}}, nil)
	s := createSession(t, m, login, id)
	if _, err := s.CheckFile(t.Context(), "/", "file"); err != fault.Error("SFTP_UNAVAILABLE") {
		t.Fatal(err)
	}
	if _, err := s.terminal.Write([]byte("no-sftp-shell-alive")); err != nil {
		t.Fatal(err)
	}
	waitBytes(t, s, []byte("no-sftp-shell-alive"))
	if s.View().Status != "DISCONNECTED" {
		t.Fatal(s.View())
	}
}

func TestSFTPCancelCleansAfterIndependentChannelLoss(t *testing.T) {
	s, directory := uploadSession(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reader, writer := io.Pipe()
	defer writer.Close()
	consumed := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		_, err := s.UploadFile(ctx, directory, "unfinished", false, FileSource{Reader: reader, Interrupt: func() { _ = reader.CloseWithError(context.Canceled) }})
		result <- err
	}()
	go func() {
		_, err := io.WriteString(writer, "partial")
		if err != nil {
			t.Error(err)
		}
		close(consumed)
	}()
	<-consumed
	s.mu.Lock()
	op := s.fileOperation
	s.mu.Unlock()
	op.mu.Lock()
	ch := op.channel
	op.mu.Unlock()
	// 先关闭独立 channel，再取消工作；临时文件只能通过新的清理 channel 删除。
	if err := ch.Close(); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := awaitFileResult(t, result); err != fault.Error("SFTP_CANCELED") {
		t.Fatal(err)
	}
	assertNoTemporary(t, directory)
	if _, err := s.terminal.Write([]byte("shell-after-cleanup-channel")); err != nil {
		t.Fatal(err)
	}
	waitBytes(t, s, []byte("shell-after-cleanup-channel"))
	if s.View().Status != "DISCONNECTED" {
		t.Fatal(s.View())
	}
}

// 上游示例内存文件系统忽略 chmod；补齐协议权限模型，保留原有失败断言及其触发点。
type permissionMemoryFS struct {
	sftp.FileCmder
	sftp.FileLister
	mu         sync.Mutex
	modes      map[string]os.FileMode
	command    func(*sftp.Request) error
	attributes func(string, os.FileInfo) (os.FileInfo, error)
}

func permissionMemoryHandlers() (sftp.Handlers, *permissionMemoryFS) {
	h := sftp.InMemHandler()
	fs := &permissionMemoryFS{FileCmder: h.FileCmd, FileLister: h.FileList, modes: make(map[string]os.FileMode)}
	h.FileCmd, h.FileList = fs, fs
	return h, fs
}

func (fs *permissionMemoryFS) Filecmd(r *sftp.Request) error {
	if fs.command != nil {
		if err := fs.command(r); err != nil {
			return err
		}
	}
	if r.Method == "Setstat" && r.AttrFlags().Permissions {
		fs.mu.Lock()
		fs.modes[r.Filepath] = os.FileMode(r.Attributes().Mode & 07777)
		fs.mu.Unlock()
		return nil
	}
	return fs.FileCmder.Filecmd(r)
}

func (fs *permissionMemoryFS) PosixRename(r *sftp.Request) error {
	if fs.command != nil {
		if err := fs.command(r); err != nil {
			return err
		}
	}
	return fs.FileCmder.(sftp.PosixRenameFileCmder).PosixRename(r)
}

func (fs *permissionMemoryFS) Filelist(r *sftp.Request) (sftp.ListerAt, error) {
	var list sftp.ListerAt
	var err error
	if r.Method == "Lstat" {
		list, err = fs.FileLister.(sftp.LstatFileLister).Lstat(r)
	} else {
		list, err = fs.FileLister.Filelist(r)
	}
	if err != nil {
		return nil, err
	}
	return permissionLister{list, fs, r.Filepath, r.Method == "List"}, nil
}
func (fs *permissionMemoryFS) Lstat(r *sftp.Request) (sftp.ListerAt, error) { return fs.Filelist(r) }

type permissionLister struct {
	sftp.ListerAt
	fs      *permissionMemoryFS
	name    string
	listing bool
}

func (l permissionLister) ListAt(entries []os.FileInfo, offset int64) (int, error) {
	n, err := l.ListerAt.ListAt(entries, offset)
	for i := 0; i < n; i++ {
		name := l.name
		if l.listing {
			name = path.Join(name, entries[i].Name())
		}
		l.fs.mu.Lock()
		mode, exists := l.fs.modes[name]
		l.fs.mu.Unlock()
		if exists {
			entries[i] = permissionInfo{entries[i], entries[i].Mode().Type() | mode}
		}
		if l.fs.attributes != nil {
			var attributeErr error
			entries[i], attributeErr = l.fs.attributes(name, entries[i])
			if attributeErr != nil {
				return 0, attributeErr
			}
		}
	}
	return n, err
}

type permissionInfo struct {
	os.FileInfo
	mode os.FileMode
}

func (i permissionInfo) Mode() os.FileMode { return i.mode }

func memoryUploadSession(t *testing.T, h sftp.Handlers) *Session {
	t.Helper()
	m, login, id, _ := setupWith(t, testutil.TerminalOptions{FileChannel: func(incoming ssh.NewChannel, _ <-chan struct{}) {
		ch, requests, err := incoming.Accept()
		if err != nil {
			return
		}
		defer ch.Close()
		request := <-requests
		if request == nil {
			return
		}
		_ = request.Reply(true, nil)
		requestsDone := make(chan struct{})
		go func() { ssh.DiscardRequests(requests); close(requestsDone) }()
		defer func() { _ = ch.Close(); <-requestsDone }()
		srv := sftp.NewRequestServer(ch, h)
		_ = srv.Serve()
		_ = srv.Close()
	}}, nil)
	return createSession(t, m, login, id)
}

func TestSFTPPrivateStagingRealOpenSSH(t *testing.T) {
	for _, mask := range []string{"0000", "0022", "0077"} {
		t.Run(mask, func(t *testing.T) {
			directory, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err = os.Chmod(directory, 0777); err != nil {
				t.Fatal(err)
			}
			m, login, id, _ := setupWith(t, testutil.TerminalOptions{FileChannel: openSSHFileServer(t, "-u", mask)}, nil)
			s := createSession(t, m, login, id)
			written, release := make(chan struct{}), make(chan struct{})
			result := make(chan error, 1)
			go func() {
				_, e := s.UploadFile(t.Context(), directory, "new", false, FileSource{Reader: strings.NewReader("private-content"), Finish: func() error { close(written); <-release; return nil }})
				result <- e
			}()
			select {
			case <-written:
			case e := <-result:
				t.Fatal("尚未写入便结束", e)
			}
			// Finish 屏障证明内容已经写完，而提交尚未发生；无须按时间猜测上传窗口。
			matches, err := filepath.Glob(filepath.Join(directory, ".web-ssh-upload-*"))
			if err != nil || len(matches) != 1 {
				close(release)
				t.Fatal(matches, err)
			}
			stage, err := os.Lstat(matches[0])
			if err != nil || !stage.IsDir() || stage.Mode().Perm() != 0700 {
				close(release)
				t.Fatal(stage, err)
			}
			payload, err := os.Lstat(filepath.Join(matches[0], "payload"))
			if err != nil || payload.Mode().Perm() != 0600 {
				close(release)
				t.Fatal(payload, err)
			}
			data, err := os.ReadFile(filepath.Join(matches[0], "payload"))
			if err != nil || string(data) != "private-content" {
				close(release)
				t.Fatal(string(data), err)
			}
			if _, err = os.Stat(filepath.Join(directory, "new")); !os.IsNotExist(err) {
				close(release)
				t.Fatal("提前提交", err)
			}
			close(release)
			if err = awaitFileResult(t, result); err != nil {
				t.Fatal(err)
			}
			final, err := os.Stat(filepath.Join(directory, "new"))
			if err != nil || final.Mode().Perm() != 0600 {
				t.Fatal(final, err)
			}
			assertNoTemporary(t, directory)
		})
	}
}

func TestSFTPOverwritePermissionsRealOpenSSH(t *testing.T) {
	for _, mode := range []os.FileMode{0644, 0755, 0640, 0600} {
		t.Run(mode.String(), func(t *testing.T) {
			s, directory := uploadSession(t)
			target := filepath.Join(directory, "old")
			if err := os.WriteFile(target, []byte("ORIGINAL"), mode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(target, mode); err != nil {
				t.Fatal(err)
			}
			if _, err := s.UploadFile(t.Context(), directory, "old", true, source("replacement")); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(target)
			if err != nil || info.Mode().Perm() != mode {
				t.Fatal(info, err)
			}
			data, err := os.ReadFile(target)
			if err != nil || string(data) != "replacement" {
				t.Fatal(string(data), err)
			}
			assertNoTemporary(t, directory)
		})
	}
}

type uploadOwnerInfo struct {
	os.FileInfo
	uid, gid uint32
}

func (i uploadOwnerInfo) Sys() any { return &syscall.Stat_t{Uid: i.uid, Gid: i.gid} }

type uploadMissingOwnerInfo struct{ os.FileInfo }

func (i uploadMissingOwnerInfo) Sys() any { return nil }

func TestSFTPRejectUnsafeOverwrite(t *testing.T) {
	for _, failure := range []string{"setuid", "setgid", "sticky", "uid", "gid", "missing-owner", "zero-owner", "stat-error", "target-change", "late-target-change", "final-chmod", "final-verify"} {
		t.Run(failure, func(t *testing.T) {
			h, fs := permissionMemoryHandlers()
			s := memoryUploadSession(t, h)
			if err := s.withFileClient(t.Context(), func(_ context.Context, c *sftp.Client, _ func()) error {
				f, err := c.Create("/old")
				if err != nil {
					return err
				}
				_, err = f.Write([]byte("ORIGINAL"))
				closeErr := f.Close()
				if err != nil {
					return err
				}
				return closeErr
			}); err != nil {
				t.Fatal(err)
			}
			statCount, payloadStats := 0, 0
			fs.attributes = func(name string, info os.FileInfo) (os.FileInfo, error) {
				if name != "/old" {
					if strings.HasSuffix(name, "/payload") {
						payloadStats++
						if failure == "final-verify" && payloadStats == 3 {
							return permissionInfo{info, 0777}, nil
						}
					}
					return info, nil
				}
				statCount++
				switch failure {
				case "setuid":
					return permissionInfo{info, info.Mode() | os.ModeSetuid}, nil
				case "setgid":
					return permissionInfo{info, info.Mode() | os.ModeSetgid}, nil
				case "sticky":
					return permissionInfo{info, info.Mode() | os.ModeSticky}, nil
				case "uid":
					return uploadOwnerInfo{info, 123, 65534}, nil
				case "gid":
					return uploadOwnerInfo{info, 65534, 123}, nil
				case "zero-owner":
					return uploadOwnerInfo{info, 0, 0}, nil
				case "missing-owner":
					return uploadMissingOwnerInfo{info}, nil
				case "stat-error":
					return nil, sftp.ErrSSHFxPermissionDenied
				case "late-target-change":
					if statCount > 2 {
						return permissionInfo{info, 0755}, nil
					}
				case "target-change":
					if statCount > 1 {
						return permissionInfo{info, 0755}, nil
					}
				}
				return info, nil
			}
			payloadChmods := 0
			fs.command = func(r *sftp.Request) error {
				if failure == "final-chmod" && r.Method == "Setstat" && strings.HasSuffix(r.Filepath, "/payload") {
					payloadChmods++
					if payloadChmods == 2 {
						return sftp.ErrSSHFxPermissionDenied
					}
				}
				if r.Method == "PosixRename" {
					t.Error("不安全目标进入了提交")
				}
				return nil
			}
			want := fault.Error("SFTP_PERMISSION_DENIED")
			if failure == "target-change" || failure == "late-target-change" {
				want = "SFTP_FAILED"
			}
			if _, err := s.UploadFile(t.Context(), "/", "old", true, source("replacement")); err != want {
				t.Fatal("未按预期拒绝覆盖", err, want)
			}
			fs.attributes, fs.command = nil, nil
			if err := s.withFileClient(t.Context(), func(_ context.Context, c *sftp.Client, _ func()) error {
				f, err := c.Open("/old")
				if err != nil {
					return err
				}
				defer f.Close()
				data, err := io.ReadAll(f)
				if err == nil && string(data) != "ORIGINAL" {
					t.Error("原文件被破坏", string(data))
				}
				list, listErr := c.ReadDir("/")
				if listErr != nil {
					return listErr
				}
				for _, entry := range list {
					if strings.HasPrefix(entry.Name(), ".web-ssh-upload-") {
						t.Error("暂存目录未清理")
					}
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSFTPRejectUnsafeStagingBeforeReading(t *testing.T) {
	for _, failure := range []string{"mkdir", "directory-chmod", "directory-verify", "payload-chmod", "payload-verify"} {
		t.Run(failure, func(t *testing.T) {
			h, fs := permissionMemoryHandlers()
			fs.command = func(r *sftp.Request) error {
				if failure == "mkdir" && r.Method == "Mkdir" {
					return sftp.ErrSSHFxFailure
				}
				if r.Method == "Setstat" {
					payload := strings.HasSuffix(r.Filepath, "/payload")
					if failure == "directory-chmod" && !payload || failure == "payload-chmod" && payload {
						return sftp.ErrSSHFxPermissionDenied
					}
				}
				return nil
			}
			fs.attributes = func(name string, info os.FileInfo) (os.FileInfo, error) {
				if strings.Contains(name, ".web-ssh-upload-") {
					payload := strings.HasSuffix(name, "/payload")
					if failure == "directory-verify" && !payload || failure == "payload-verify" && payload {
						return permissionInfo{info, info.Mode().Type() | 0777}, nil
					}
				}
				return info, nil
			}

			s := memoryUploadSession(t, h)
			reads := 0
			reader := readerFunction(func([]byte) (int, error) { reads++; return 0, io.EOF })
			if _, err := s.UploadFile(t.Context(), "/", "new", false, FileSource{Reader: reader}); err == nil {
				t.Fatal("不安全暂存条件未拒绝")
			}
			if reads != 0 {
				t.Fatal("权限确认前读取了上传内容", reads)
			}
		})
	}
}

type readerFunction func([]byte) (int, error)

func (f readerFunction) Read(p []byte) (int, error) { return f(p) }

func TestSFTPCommitOutcomeSurvivesCleanup(t *testing.T) {
	for _, event := range []string{"cancel-after-commit", "cleanup-denied", "commit-response-lost"} {
		t.Run(event, func(t *testing.T) {
			h, fs := permissionMemoryHandlers()
			s := memoryUploadSession(t, h)
			cleanupEntered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			fs.command = func(r *sftp.Request) error {
				if event == "commit-response-lost" && r.Method == "Link" {
					// 服务端已执行，但明确成功响应未送达客户端，不能推测成功。
					if err := fs.FileCmder.Filecmd(r); err != nil {
						return err
					}
					return sftp.ErrSSHFxConnectionLost
				}
				if r.Method == "Remove" || r.Method == "Rmdir" {
					once.Do(func() { close(cleanupEntered); <-release })
					if event == "cleanup-denied" {
						return sftp.ErrSSHFxPermissionDenied
					}
				}
				return nil
			}
			finished := make(chan error, 1)
			go func() { _, err := s.UploadFile(t.Context(), "/", "new", false, source("COMMITTED")); finished <- err }()
			select {
			case <-cleanupEntered:
			case err := <-finished:
				t.Fatal("未进入清理", err)
			}
			s.mu.Lock()
			op := s.fileOperation
			s.mu.Unlock()
			if op.hasCommitted() != (event != "commit-response-lost") {
				close(release)
				t.Fatal("提交确认时机错误")
			}
			if event == "cancel-after-commit" {
				op.cancel()
			}
			select {
			case err := <-finished:
				close(release)
				t.Fatal("清理结束前返回", err)
			default:
			}
			close(release)
			err := awaitFileResult(t, finished)
			if event == "commit-response-lost" {
				if err != fault.Error("SFTP_UNAVAILABLE") {
					t.Fatal("未确认提交却报告成功", err)
				}
			} else if err != nil {
				t.Fatal("已提交成功被改写", err)
			}
			fs.command = nil
			if err = s.withFileClient(t.Context(), func(_ context.Context, c *sftp.Client, _ func()) error {
				file, err := c.Open("/new")
				if err != nil {
					return err
				}
				defer file.Close()
				data, err := io.ReadAll(file)
				if err == nil && string(data) != "COMMITTED" {
					t.Error("提交内容错误", string(data))
				}
				entries, listErr := c.ReadDir("/")
				if listErr != nil {
					return listErr
				}
				stagingCount := 0
				for _, entry := range entries {
					if strings.HasPrefix(entry.Name(), ".web-ssh-upload-") {
						stagingCount++
					}
				}
				if event == "cleanup-denied" && stagingCount != 1 {
					t.Error("未保留清理失败的真实状态", stagingCount)
				}
				if event != "cleanup-denied" && stagingCount != 0 {
					t.Error("成功清理后仍有残留", stagingCount)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if s.View().Status != "DISCONNECTED" {
				t.Fatal("普通清理错误关闭了 Shell", s.View())
			}
		})
	}
}

func TestSFTPCancelDuringPermissionChange(t *testing.T) {
	h, fs := permissionMemoryHandlers()
	entered, release := make(chan struct{}), make(chan struct{})
	fs.command = func(r *sftp.Request) error {
		if r.Method == "Setstat" && strings.HasSuffix(r.Filepath, "/payload") {
			close(entered)
			<-release
		}
		if r.Method == "Link" {
			t.Error("取消后仍提交")
		}
		return nil
	}
	s := memoryUploadSession(t, h)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	finished := make(chan error, 1)
	go func() { _, err := s.UploadFile(ctx, "/", "new", false, source("content")); finished <- err }()
	<-entered
	cancel()
	close(release)
	if err := awaitFileResult(t, finished); err != fault.Error("SFTP_CANCELED") {
		t.Fatal(err)
	}
	fs.command = nil
	if err := s.withFileClient(t.Context(), func(_ context.Context, c *sftp.Client, _ func()) error {
		entries, err := c.ReadDir("/")
		if err == nil && len(entries) != 0 {
			t.Error("取消时未清理", entries)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.terminal.Write([]byte("shell-after-permission-cancel")); err != nil {
		t.Fatal(err)
	}
	waitBytes(t, s, []byte("shell-after-permission-cancel"))
}

func TestSFTPMkdirCollisionDoesNotAcquireOwnership(t *testing.T) {
	h, fs := permissionMemoryHandlers()
	var collision string
	fs.command = func(r *sftp.Request) error {
		if r.Method == "Mkdir" {
			// 在服务端模拟名称已被其他创建者占用，客户端只能得到失败。
			if err := fs.FileCmder.Filecmd(r); err != nil {
				return err
			}
			collision = r.Filepath
			return sftp.ErrSSHFxFailure
		}
		if r.Method == "Remove" || r.Method == "Rmdir" || r.Method == "Setstat" {
			t.Error("操作了未取得所有权的目录", r.Method)
		}
		return nil
	}
	s := memoryUploadSession(t, h)
	if _, err := s.UploadFile(t.Context(), "/", "new", false, source("content")); err == nil {
		t.Fatal("名称碰撞未拒绝")
	}
	fs.command = nil
	if collision == "" {
		t.Fatal("未进入 Mkdir")
	}
	if err := s.withFileClient(t.Context(), func(_ context.Context, c *sftp.Client, _ func()) error {
		info, err := c.Lstat(collision)
		if err == nil && !info.IsDir() {
			t.Error("碰撞目录被改变")
		}
		return err
	}); err != nil {
		t.Fatal("碰撞目录被清理", err)
	}
}

func TestSFTPUnknownStagingObjectIsNotRemoved(t *testing.T) {
	h, fs := permissionMemoryHandlers()
	var unknown string
	fs.command = func(r *sftp.Request) error {
		if r.Method == "Setstat" {
			unknown = path.Join(r.Filepath, "unknown")
			request := sftp.NewRequest("Put", unknown)
			request.Flags = 2 | 8 | 32
			file, err := h.FilePut.Filewrite(request)
			if err != nil {
				return err
			}
			if closer, ok := file.(io.Closer); ok {
				return closer.Close()
			}
		}
		if r.Method == "Remove" {
			t.Error("清理了未知对象", r.Filepath)
		}
		return nil
	}
	s := memoryUploadSession(t, h)
	if _, err := s.UploadFile(t.Context(), "/", "new", false, source("content")); err == nil {
		t.Fatal("没有拒绝非空暂存目录")
	}
	fs.command = nil
	if err := s.withFileClient(t.Context(), func(_ context.Context, c *sftp.Client, _ func()) error {
		_, err := c.Lstat(unknown)
		return err
	}); err != nil {
		t.Fatal("删除了未知对象", err)
	}
}
