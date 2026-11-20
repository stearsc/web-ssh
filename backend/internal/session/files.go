package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"os"
	"path"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/pkg/sftp"
	"github.com/stearsc/web-ssh/backend/internal/fault"
	"golang.org/x/crypto/ssh"
)

const MaxUploadSize int64 = 512 << 20

type FileCheck struct {
	Exists bool   `json:"exists"`
	Path   string `json:"path"`
}

type FileUpload struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// FileSource 由 Handler 持有 HTTP 资源；Interrupt 必须解除其正在进行的读取。
// Finish 校验 multipart 尾部，成功前不允许提交远端文件。
type FileSource struct {
	Reader    io.Reader
	Finish    func() error
	Interrupt func()
}

func validateFilePath(directory, filename string) (string, error) {
	if !utf8.ValidString(filename) || filename == "" || filename == "." || filename == ".." || strings.ContainsAny(filename, "/\\") || len(filename) > 255 || strings.IndexFunc(filename, unicode.IsControl) >= 0 {
		return "", fault.Error("SFTP_INVALID_PATH")
	}
	if !utf8.ValidString(directory) || !path.IsAbs(directory) || strings.ContainsAny(directory, "\\") || len(directory) > 4096 || strings.IndexFunc(directory, unicode.IsControl) >= 0 {
		return "", fault.Error("SFTP_INVALID_PATH")
	}
	for _, part := range strings.Split(directory, "/") {
		if part == ".." {
			return "", fault.Error("SFTP_INVALID_PATH")
		}
	}
	return path.Clean(directory), nil
}

// 路径安全依赖运行环境保证：上传期间没有其他进程并发修改目标及其父路径。
// RealPath/Lstat 与提交不是事务；pkg/sftp v1.13.12 的 PosixRename 只保证最终替换原子性，
// 不提供抵御恶意并发路径替换的隔离。发现路径类型异常时直接拒绝，不进行危险降级。
func checkFileTarget(c *sftp.Client, directory, filename string, progress func()) (FileCheck, error) {
	directory, err := validateFilePath(directory, filename)
	if err != nil {
		return FileCheck{}, err
	}
	current := "/"
	parts := strings.Split(strings.TrimPrefix(directory, "/"), "/")
	for i := -1; i < len(parts); i++ {
		if i >= 0 {
			if parts[i] == "" {
				continue
			}
			current = path.Join(current, parts[i])
		}
		info, e := c.Lstat(current)
		progress()
		if e != nil {
			if os.IsNotExist(e) {
				return FileCheck{}, fault.Error("SFTP_DIRECTORY_UNAVAILABLE")
			}
			return FileCheck{}, fileError(e)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return FileCheck{}, fault.Error("SFTP_INVALID_PATH")
		}
	}
	canonical, err := c.RealPath(directory)
	progress()
	if err != nil {
		return FileCheck{}, fileError(err)
	}
	if canonical != directory {
		return FileCheck{}, fault.Error("SFTP_INVALID_PATH")
	}
	target := path.Join(directory, filename)
	info, err := c.Lstat(target)
	progress()
	if os.IsNotExist(err) {
		return FileCheck{Path: target}, nil
	}
	if err != nil {
		return FileCheck{}, fileError(err)
	}
	if !info.Mode().IsRegular() {
		return FileCheck{}, fault.Error("SFTP_INVALID_PATH")
	}
	return FileCheck{Exists: true, Path: target}, nil
}

func fileError(err error) error {
	if err == nil {
		return nil
	}
	var business fault.Error
	if errors.As(err, &business) {
		return business
	}
	if os.IsPermission(err) {
		return fault.Error("SFTP_PERMISSION_DENIED")
	}
	var status *sftp.StatusError
	if errors.As(err, &status) {
		switch status.Code {
		case 3:
			return fault.Error("SFTP_PERMISSION_DENIED")
		case 6, 7:
			return fault.Error("SFTP_UNAVAILABLE")
		case 14, 15:
			return fault.Error("SFTP_NO_SPACE")
		}
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, sftp.ErrSSHFxConnectionLost) || errors.Is(err, sftp.ErrSSHFxNoConnection) || errors.Is(err, net.ErrClosed) {
		return fault.Error("SFTP_UNAVAILABLE")
	}
	return fault.Error("SFTP_FAILED")
}

// FindForFiles 保留短期的上传异常回执，使已 abort 的浏览器也能在完成屏障中获知原因。
// 这不是 Session 状态或进度接口；回执仅用于文件 API，过期由既有 Sweep 清除。
func (m *Manager) FindForFiles(ctx context.Context, id string) (*Session, error) {
	m.mu.Lock()
	until := m.fileFailures[id]
	s := m.sessions[id]
	m.mu.Unlock()
	if m.now().Before(until) {
		if s != nil {
			if err := s.WaitFileOperation(ctx); err != nil && err != fault.Error("SFTP_CANCEL_FAILED") {
				return nil, err
			}
		}
		return nil, fault.Error("SFTP_CANCEL_FAILED")
	}
	return m.Find(id)
}

func (s *Session) recordFileFailure() {
	m := s.manager
	m.mu.Lock()
	if m.fileFailures == nil {
		m.fileFailures = make(map[string]time.Time)
	}
	budget := m.cfg.Server.RequestTimeout + m.cfg.Ssh.ConnectTimeout + 2*m.cfg.Ssh.OperationTimeout
	m.fileFailures[s.id] = m.now().Add(time.Duration(budget) * time.Second)
	m.mu.Unlock()
}

func (s *Session) CheckFile(ctx context.Context, directory, filename string) (FileCheck, error) {
	var result FileCheck
	if _, err := validateFilePath(directory, filename); err != nil {
		return result, err
	}
	if err := s.WaitFileOperation(ctx); err != nil {
		return result, err
	}
	err := s.withFileClient(ctx, func(ctx context.Context, c *sftp.Client, progress func()) error {
		var err error
		result, err = checkFileTarget(c, directory, filename, progress)
		return err
	})
	return result, err
}

func (s *Session) UploadFile(ctx context.Context, directory, filename string, overwrite bool, source FileSource) (FileUpload, error) {
	var result FileUpload
	directory, err := validateFilePath(directory, filename)
	if err != nil {
		return result, err
	}
	err = s.withFileClient(ctx, func(ctx context.Context, c *sftp.Client, progress func()) error {
		interrupted := make(chan struct{})
		stop := context.AfterFunc(ctx, func() {
			if source.Interrupt != nil {
				source.Interrupt()
			}
			close(interrupted)
		})
		defer func() {
			if !stop() {
				<-interrupted
			}
		}()
		target, err := checkFileTarget(c, directory, filename, progress)
		if err != nil {
			return err
		}
		if target.Exists && !overwrite {
			return fault.Error("SFTP_FILE_EXISTS")
		}
		extension := "hardlink@openssh.com"
		if overwrite {
			extension = "posix-rename@openssh.com"
		}
		if version, ok := c.HasExtension(extension); !ok || version != "1" {
			return fault.Error("SFTP_ATOMIC_UNAVAILABLE")
		}
		random := make([]byte, 16)
		if _, err = rand.Read(random); err != nil {
			return fault.Error("INTERNAL_ERROR")
		}
		temporary := path.Join(directory, ".web-ssh-upload-"+hex.EncodeToString(random))
		file, err := c.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
		progress()
		if err != nil {
			return fileError(err)
		}
		isClosed := false
		defer func() {
			s.mu.Lock()
			op := s.fileOperation
			s.mu.Unlock()
			op.cleanupOnce.Do(func() { close(op.cleanup) })
			if !isClosed {
				if e := file.Close(); e != nil {
					s.manager.log.Warn("上传临时文件关闭失败", "session_id", s.id)
				}
			}
			cleanupErr := c.Remove(temporary)
			if fileError(cleanupErr) == fault.Error("SFTP_UNAVAILABLE") && s.ctx.Err() == nil {
				// 原 channel 已失效时，清理仍归当前 operation 所有，不重新登录或重试上传。
				cleanupErr = s.removeTemporaryOnChannel(op, temporary)
			}
			if cleanupErr != nil && !os.IsNotExist(cleanupErr) {
				s.manager.log.Warn("上传临时文件清理失败", "session_id", s.id, "temporary", temporary)
			}
		}()
		// 只限制文件内容；multipart 首部、目录字段及边界不计入 512 MiB。
		limited := &io.LimitedReader{R: source.Reader, N: MaxUploadSize + 1}
		buffer := make([]byte, 64<<10)
		var size int64
		for {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			n, readErr := limited.Read(buffer)
			if n > 0 {
				progress()
				if size+int64(n) > MaxUploadSize {
					return fault.Error("SFTP_TOO_LARGE")
				}
				written, writeErr := file.Write(buffer[:n])
				progress()
				if writeErr != nil {
					return fileError(writeErr)
				}
				if written != n {
					return fault.Error("SFTP_FAILED")
				}
				size += int64(n)
			}
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				return fault.Error("SFTP_READ_FAILED")
			}
		}
		if err = file.Close(); err != nil {
			isClosed = true
			return fileError(err)
		}
		isClosed = true
		progress()
		info, err := c.Lstat(temporary)
		progress()
		if err != nil {
			return fileError(err)
		}
		if !info.Mode().IsRegular() || info.Size() != size {
			return fault.Error("SFTP_FAILED")
		}
		if source.Finish != nil {
			if err = source.Finish(); err != nil {
				return err
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// 提交前再次检查；这不替代运行环境的路径稳定前提。
		target, err = checkFileTarget(c, directory, filename, progress)
		if err != nil {
			return err
		}
		if target.Exists && !overwrite {
			return fault.Error("SFTP_FILE_EXISTS")
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if overwrite {
			err = c.PosixRename(temporary, target.Path)
		} else {
			// hardlink 扩展对已存在目标返回错误，避免不同服务端 Rename 覆盖语义不一致。
			err = c.Link(temporary, target.Path)
		}
		progress()
		if err != nil {
			return fileError(err)
		}
		result = FileUpload{Path: target.Path, Size: size}
		return nil
	})
	return result, err
}

// removeTemporaryOnChannel 同步运行于原工作协程，沿用原清理看门狗。
// channel-open 或关闭阻塞仍由唯一 Session.Close/reap 兜底，不遗留独立后台任务。
func (s *Session) removeTemporaryOnChannel(op *fileOperation, temporary string) error {
	ch, requests, err := s.terminal.OpenFileChannel()
	if err != nil {
		return err
	}
	op.mu.Lock()
	op.channel = ch
	op.mu.Unlock()
	requestsDone := make(chan struct{})
	go func() { ssh.DiscardRequests(requests); close(requestsDone) }()
	var client *sftp.Client
	defer func() {
		if client != nil {
			_ = client.Close()
		} else {
			_ = ch.Close()
		}
		<-requestsDone
	}()
	accepted, err := ch.SendRequest("subsystem", true, ssh.Marshal(struct{ Name string }{"sftp"}))
	if err != nil {
		return err
	}
	if !accepted {
		return fault.Error("SFTP_UNAVAILABLE")
	}
	client, err = sftp.NewClientPipe(ch, ch)
	if err != nil {
		return err
	}
	return client.Remove(temporary)
}
