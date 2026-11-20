package server

import (
	"encoding/json"
	"net/netip"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/stearsc/web-ssh/backend/internal/fault"
)

type Input struct {
	AuthType          string `json:"auth_type,omitempty"`
	Passphrase        string `json:"passphrase,omitempty"`
	PrivateKey        []byte `json:"-"`
	PasswordPresent   bool   `json:"-"`
	PassphrasePresent bool   `json:"-"`
	Name              string `json:"name"`
	Host              string `json:"host"`
	Port              *int   `json:"port,omitempty"`
	Username          string `json:"username"`
	Password          string `json:"password,omitempty"`
	GroupName         string `json:"group_name,omitempty"`
	Remark            string `json:"remark,omitempty"`
}
type HostKeyView struct {
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Algorithm   string `json:"algorithm"`
	Fingerprint string `json:"fingerprint"`
}
type View struct {
	AuthType             string       `json:"auth_type"`
	CredentialConfigured bool         `json:"credential_configured"`
	ID                   string       `json:"id"`
	Name                 string       `json:"name"`
	Host                 string       `json:"host"`
	Port                 int          `json:"port"`
	Username             string       `json:"username"`
	GroupName            string       `json:"group_name"`
	Remark               string       `json:"remark"`
	HostKey              *HostKeyView `json:"host_key"`
	CreatedAt            string       `json:"created_at"`
	UpdatedAt            string       `json:"updated_at"`
}
type Record struct {
	AuthType, PrivateKeyObjectID                string
	PassphraseEncrypted                         []byte
	ID, Name, Host, Username, GroupName, Remark string
	Port                                        int
	PasswordEncrypted                           []byte
	PublicKey                                   []byte
	Algorithm, Fingerprint                      string
	CreatedAt, UpdatedAt                        int64
}

func Timestamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }
func (r Record) view() View {
	v := View{AuthType: r.AuthType, CredentialConfigured: (r.AuthType == "password" && len(r.PasswordEncrypted) > 0) || (r.AuthType == "private_key" && r.PrivateKeyObjectID != ""), ID: r.ID, Name: r.Name, Host: r.Host, Port: r.Port, Username: r.Username, GroupName: r.GroupName, Remark: r.Remark, CreatedAt: Timestamp(time.UnixMilli(r.CreatedAt)), UpdatedAt: Timestamp(time.UnixMilli(r.UpdatedAt))}
	if len(r.PublicKey) > 0 {
		v.HostKey = &HostKeyView{r.Host, r.Port, r.Algorithm, r.Fingerprint}
	}
	return v
}

var dnsLabel = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

func validate(in Input, create bool) (Input, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.GroupName = strings.TrimSpace(in.GroupName)
	in.Host = strings.ToLower(strings.TrimSpace(in.Host))
	if in.Port == nil {
		if !create {
			return in, fault.Error("INVALID_REQUEST")
		}
		port := 22
		in.Port = &port
	}
	if !utf8.ValidString(in.Name+in.Host+in.Username+in.Password+in.GroupName+in.Remark) || len(in.Name) < 1 || len(in.Name) > 128 || len(in.Host) < 1 || len(in.Host) > 253 || *in.Port < 1 || *in.Port > 65535 || len(in.Username) < 1 || len(in.Username) > 128 || strings.ContainsRune(in.Username, 0) || len(in.Password) > 4096 || len(in.Passphrase) > 4096 || !utf8.ValidString(in.Passphrase) || (in.AuthType != "" && in.AuthType != "password" && in.AuthType != "private_key") || len(in.GroupName) > 128 || len(in.Remark) > 1024 {
		return in, fault.Error("INVALID_REQUEST")
	}
	if ip, err := netip.ParseAddr(in.Host); err == nil {
		if ip.Zone() != "" {
			return in, fault.Error("INVALID_REQUEST")
		}
		in.Host = ip.String()
	} else {
		for _, label := range strings.Split(strings.TrimSuffix(in.Host, "."), ".") {
			if !dnsLabel.MatchString(label) {
				return in, fault.Error("INVALID_REQUEST")
			}
		}
	}
	return in, nil
}

// UnmarshalJSON 保留字段存在性，私钥模式即使显式提交空 password 也必须拒绝。
func (in *Input) UnmarshalJSON(data []byte) error {
	type plain Input
	var value plain
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	*in = Input(value)
	_, in.PasswordPresent = fields["password"]
	_, in.PassphrasePresent = fields["passphrase"]
	return nil
}
