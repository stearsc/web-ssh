package httpapi

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"

	"github.com/stearsc/web-ssh/backend/internal/config"
	"github.com/stearsc/web-ssh/backend/internal/fault"
	servers "github.com/stearsc/web-ssh/backend/internal/server"
	"golang.org/x/crypto/ssh"
)

type uploadPart struct {
	name, body string
	header     textproto.MIMEHeader
}

func multipartRequest(t *testing.T, parts []uploadPart) ([]byte, string) {
	t.Helper()
	var data bytes.Buffer
	writer := multipart.NewWriter(&data)
	for _, part := range parts {
		header := part.header
		if header == nil {
			header = make(textproto.MIMEHeader)
			header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"`, part.name))
			if part.name == "metadata" {
				header.Set("Content-Type", "application/json")
			} else {
				header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="key.pem"`, part.name))
				header.Set("Content-Type", "application/octet-stream")
			}
		}
		out, err := writer.CreatePart(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = io.WriteString(out, part.body); err != nil {
			t.Fatal(err)
		}
	}
	writer.Close()
	return data.Bytes(), writer.FormDataContentType()
}

func TestMultipartExactPartsHeadersAndByteBudgets(t *testing.T) {
	metadata := `{"name":"key","host":"localhost","username":"tester","auth_type":"private_key"}`
	parts := []uploadPart{{name: "metadata", body: metadata}, {name: "private_key", body: "raw-key"}}
	for _, order := range [][]uploadPart{parts, {parts[1], parts[0]}} {
		raw, contentType := multipartRequest(t, order)
		request := httptest.NewRequest("POST", "/api/servers", bytes.NewReader(raw))
		request.Header.Set("Content-Type", contentType)
		m := Management{Config: config.Defaults()}
		in, release, err := m.serverInput(httptest.NewRecorder(), request)
		if err != nil || string(in.PrivateKey) != "raw-key" || in.AuthType != "private_key" {
			t.Fatal(in, err)
		}
		release()
	}
	cases := map[string][]uploadPart{
		"缺 metadata": {parts[1]}, "缺私钥": {parts[0]}, "重复": {parts[0], parts[0], parts[1]}, "未知": {parts[0], {name: "extra", body: "x"}},
		"空文件":         {parts[0], {name: "private_key", body: ""}},
		"文件超限":        {parts[0], {name: "private_key", body: strings.Repeat("x", 65537)}},
		"metadata 超限": {{name: "metadata", body: strings.Repeat(" ", 48*1024) + metadata}, parts[1]},
		"password 歧义": {{name: "metadata", body: strings.TrimSuffix(metadata, "}") + `,"password":""}`}, parts[1]},
		"无认证方式":       {{name: "metadata", body: `{"name":"key","host":"localhost","username":"tester"}`}, parts[1]},
		"重复 JSON":     {{name: "metadata", body: strings.TrimSuffix(metadata, "}") + `,"name":"other"}`}, parts[1]},
		"口令超限":        {{name: "metadata", body: strings.TrimSuffix(metadata, "}") + `,"passphrase":"` + strings.Repeat("中", 1366) + `"}`}, parts[1]},
	}
	for _, headerCase := range []string{"cte", "unknown", "duplicate", "too-large", "count"} {
		header := textproto.MIMEHeader{"Content-Disposition": {`form-data; name="private_key"; filename="key"`}}
		switch headerCase {
		case "cte":
			header.Set("Content-Transfer-Encoding", "quoted-printable")
		case "unknown":
			header.Set("X-Unknown", "x")
		case "duplicate":
			header["Content-Type"] = []string{"application/octet-stream", "application/octet-stream"}
		case "too-large":
			header.Set("Content-Type", strings.Repeat("x", 8192))
		case "count":
			for i := range 9 {
				header.Set(fmt.Sprintf("X-%d", i), "x")
			}
		}
		cases[headerCase] = []uploadPart{parts[0], {name: "private_key", body: "raw-key", header: header}}
	}
	for name, parts := range cases {
		t.Run(name, func(t *testing.T) {
			raw, contentType := multipartRequest(t, parts)
			r := httptest.NewRequest("POST", "/api/servers", bytes.NewReader(raw))
			r.Header.Set("Content-Type", contentType)
			m := Management{Config: config.Defaults()}
			_, release, err := m.serverInput(httptest.NewRecorder(), r)
			release()
			if err == nil {
				t.Fatal("非法上传被接受")
			}
		})
	}
	raw, contentType := multipartRequest(t, parts)
	for _, body := range [][]byte{raw[:len(raw)-8], append(raw, bytes.Repeat([]byte{'x'}, 128*1024)...)} {
		r := httptest.NewRequest("POST", "/api/servers", bytes.NewReader(body))
		r.Header.Set("Content-Type", contentType)
		m := Management{Config: config.Defaults()}
		_, release, err := m.serverInput(httptest.NewRecorder(), r)
		release()
		if err == nil {
			t.Fatal("截断或总量超限被接受")
		}
	}
}

func TestUploadAdmissionImmediateBusyAndReleasesAfterInvalidRequest(t *testing.T) {
	uploadSlots <- struct{}{}
	uploadSlots <- struct{}{}
	defer func() { <-uploadSlots; <-uploadSlots }()
	m := Management{Config: config.Defaults()}
	r := httptest.NewRequest("POST", "/api/servers", strings.NewReader("never read"))
	r.Header.Set("Content-Type", "multipart/form-data; boundary=bound")
	_, release, err := m.serverInput(httptest.NewRecorder(), r)
	release()
	if err != fault.Error("PRIVATE_KEY_UPLOAD_BUSY") {
		t.Fatal(err)
	}
}

func TestPrivateKeyMultipartSavedThroughExistingEndpoint(t *testing.T) {
	h, m, _ := api(t)
	cookie := loginCookie(t, h, m.Config.Server.PublicOrigin)
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	block, _ := ssh.MarshalPrivateKey(key, "")
	raw := pem.EncodeToMemory(block)
	metadata := `{"name":"key","host":"localhost","username":"tester","auth_type":"private_key"}`
	body, contentType := multipartRequest(t, []uploadPart{{name: "metadata", body: metadata}, {name: "private_key", body: string(raw)}})
	r := httptest.NewRequest("POST", "/api/servers", bytes.NewReader(body))
	r.Header.Set("Origin", m.Config.Server.PublicOrigin)
	r.Header.Set("Content-Type", contentType)
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
	v := decode[servers.View](t, w)
	if v.AuthType != "private_key" || !v.CredentialConfigured {
		t.Fatal(v)
	}
	for _, secret := range []string{"PRIVATE KEY", "passphrase", `"password":`, "object_id", "ssh-keys"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("响应泄漏", secret)
		}
	}
	edit := map[string]any{"name": "renamed", "host": "localhost", "port": 22, "username": "tester"}
	data, _ := json.Marshal(edit)
	w = call(t, h, "PUT", "/api/servers/"+v.ID, string(data), cookie, m.Config.Server.PublicOrigin)
	if w.Code != 200 || decode[servers.View](t, w).AuthType != "private_key" {
		t.Fatal("普通编辑丢失私钥", w.Code, w.Body.String())
	}
	edit["password"] = ""
	data, _ = json.Marshal(edit)
	if w = call(t, h, "PUT", "/api/servers/"+v.ID, string(data), cookie, m.Config.Server.PublicOrigin); w.Code != 400 {
		t.Fatal("含糊接受空 password", w.Code)
	}
	edit["auth_type"] = "password"
	edit["password"] = "ssh-secret"
	data, _ = json.Marshal(edit)
	w = call(t, h, "PUT", "/api/servers/"+v.ID, string(data), cookie, m.Config.Server.PublicOrigin)
	if w.Code != 200 || decode[servers.View](t, w).AuthType != "password" {
		t.Fatal(w.Code, w.Body.String())
	}
}
