package httpapi

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/stearsc/web-ssh/backend/internal/fault"
	"github.com/stearsc/web-ssh/backend/internal/privatekey"
	servers "github.com/stearsc/web-ssh/backend/internal/server"
)

const multipartLimit int64 = 128 * 1024
const metadataLimit int64 = 48 * 1024

// 与解析/KDF 名额独立。持有至 handler 真正结束，避免接收后的请求在业务锁前积压秘密缓冲。
var uploadSlots = make(chan struct{}, 2)

func (m *Management) serverInput(w http.ResponseWriter, r *http.Request) (servers.Input, func(), error) {
	in := servers.Input{}
	noRelease := func() {}
	kind, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return in, noRelease, fault.Error("INVALID_REQUEST")
	}
	if kind == "application/json" {
		err = m.decode(w, r, &in, false)
		if err == nil && (in.PassphrasePresent || in.Passphrase != "") {
			err = fault.Error("INVALID_REQUEST")
		}
		return in, noRelease, err
	}
	if kind != "multipart/form-data" || len(params) != 1 || params["boundary"] == "" || len(params["boundary"]) > 70 {
		return in, noRelease, fault.Error("INVALID_REQUEST")
	}
	select {
	case uploadSlots <- struct{}{}:
	default:
		return in, noRelease, fault.Error("PRIVATE_KEY_UPLOAD_BUSY")
	}
	release := func() { <-uploadSlots }
	reader := http.MaxBytesReader(w, r.Body, multipartLimit)
	defer reader.Close()
	data, err := io.ReadAll(reader)
	if err != nil {
		clear(data)
		release()
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return in, noRelease, fault.Error("MESSAGE_TOO_LARGE")
		}
		return in, noRelease, fault.Error("INVALID_REQUEST")
	}
	// NextRawPart 内部 MIME 默认允许更大的 headers。先在有界原始输入上验证 header 行，
	// 防止归一化吞掉重复字段，也不使用会向系统临时目录溢写的 ReadForm。
	if err := checkMultipartHeaders(data, params["boundary"]); err != nil {
		clear(data)
		release()
		return in, noRelease, err
	}
	parts := multipart.NewReader(bytes.NewReader(data), params["boundary"])
	seen := map[string]bool{}
	var keyRaw []byte
	for {
		part, partErr := parts.NextRawPart()
		if partErr == io.EOF {
			break
		}
		if partErr != nil {
			err = fault.Error("INVALID_REQUEST")
			break
		}
		name := part.FormName()
		if seen[name] || (name != "metadata" && name != "private_key") {
			err = fault.Error("INVALID_REQUEST")
			break
		}
		seen[name] = true
		if name == "metadata" {
			_, disposition, dispErr := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
			filename := disposition["filename"]
			if dispErr != nil || filename != "" {
				err = fault.Error("INVALID_REQUEST")
				break
			}
			typ, metaParams, metaErr := mime.ParseMediaType(part.Header.Get("Content-Type"))
			if metaErr != nil || typ != "application/json" || len(metaParams) > 1 || (len(metaParams) == 1 && !strings.EqualFold(metaParams["charset"], "utf-8")) {
				err = fault.Error("INVALID_REQUEST")
				break
			}
			meta := r.Clone(r.Context())
			meta.Header = r.Header.Clone()
			meta.Header.Set("Content-Type", "application/json")
			meta.Body = io.NopCloser(part)
			err = DecodeJSON(w, meta, &in, metadataLimit, false)
			if err != nil {
				break
			}
		} else {
			if value := part.Header.Get("Content-Type"); value != "" {
				kind, _, typeErr := mime.ParseMediaType(value)
				if typeErr != nil || strings.HasPrefix(kind, "multipart/") {
					err = fault.Error("INVALID_REQUEST")
					break
				}
			}
			_, disposition, dispErr := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
			if dispErr != nil || disposition["filename"] == "" {
				err = fault.Error("INVALID_REQUEST")
				break
			}
			raw, readErr := io.ReadAll(io.LimitReader(part, privatekey.MaxFileSize+1))
			if readErr != nil || len(raw) == 0 {
				clear(raw)
				err = fault.Error("INVALID_REQUEST")
				break
			}
			if len(raw) > privatekey.MaxFileSize {
				clear(raw)
				err = fault.Error("MESSAGE_TOO_LARGE")
				break
			}
			// metadata 允许出现在文件之后；不要让后续 JSON 解码覆盖已接收的文件。
			keyRaw = raw
		}
	}
	// metadata 解码的自定义 Unmarshal 会替换 Input，因此单独保留文件再组装。
	in.PrivateKey = keyRaw
	if err == nil && len(in.Passphrase) > privatekey.MaxPassphraseSize {
		err = fault.Error("MESSAGE_TOO_LARGE")
	}
	if err == nil && (!seen["metadata"] || !seen["private_key"] || in.AuthType != "private_key" || in.PasswordPresent || in.Password != "") {
		err = fault.Error("INVALID_REQUEST")
	}
	clear(data)
	if err != nil {
		clear(in.PrivateKey)
		release()
		return servers.Input{}, noRelease, err
	}
	return in, func() { clear(in.PrivateKey); in.Passphrase = ""; release() }, nil
}

// checkMultipartHeaders 在标准库读取前对原始 header 字节/行数/重复项施加预算。
// 仅接受浏览器产生的 CRLF 格式；私钥 body 不规范化，所有字节原样传递。
func checkMultipartHeaders(data []byte, boundary string) error {
	invalid := fault.Error("INVALID_REQUEST")
	tooLarge := fault.Error("MESSAGE_TOO_LARGE")
	marker := []byte("--" + boundary)
	if !bytes.HasPrefix(data, append(bytes.Clone(marker), '\r', '\n')) {
		return invalid
	}
	offset := len(marker) + 2
	headerBytes := 0
	count := 0
	for {
		count++
		if count > 2 {
			return invalid
		}
		end := bytes.Index(data[offset:], []byte("\r\n\r\n"))
		if end < 0 {
			return invalid
		}
		headerBytes += end + 4
		if headerBytes > 8192 {
			return tooLarge
		}
		lines := bytes.Split(data[offset:offset+end], []byte("\r\n"))
		if len(lines) > 8 {
			return tooLarge
		}
		seen := map[string]bool{}
		for _, line := range lines {
			name, _, ok := bytes.Cut(line, []byte(":"))
			key := strings.ToLower(string(name))
			if !ok || seen[key] || (key != "content-disposition" && key != "content-type") {
				return invalid
			}
			if bytes.ContainsAny(line, "\r\n") {
				return invalid
			}
			seen[key] = true
		}
		if !seen["content-disposition"] {
			return invalid
		}
		bodyStart := offset + end + 4
		next := bytes.Index(data[bodyStart:], append([]byte("\r\n"), marker...))
		if next < 0 {
			return invalid
		}
		offset = bodyStart + next + 2 + len(marker)
		if bytes.HasPrefix(data[offset:], []byte("--")) {
			rest := data[offset+2:]
			if len(rest) != 0 && !bytes.Equal(rest, []byte("\r\n")) {
				return invalid
			}
			return nil
		}
		if !bytes.HasPrefix(data[offset:], []byte("\r\n")) {
			return invalid
		}
		offset += 2
	}
}
