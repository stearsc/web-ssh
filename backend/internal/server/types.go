package server

import (
	"net/netip"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/stearsc/web-ssh/backend/internal/fault"
)

type Input struct {
	Name      string `json:"name"`
	Host      string `json:"host"`
	Port      *int   `json:"port,omitempty"`
	Username  string `json:"username"`
	Password  string `json:"password,omitempty"`
	GroupName string `json:"group_name,omitempty"`
	Remark    string `json:"remark,omitempty"`
}
type HostKeyView struct {
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Algorithm   string `json:"algorithm"`
	Fingerprint string `json:"fingerprint"`
}
type View struct {
	ID        string       `json:"id"`
	Name      string       `json:"name"`
	Host      string       `json:"host"`
	Port      int          `json:"port"`
	Username  string       `json:"username"`
	GroupName string       `json:"group_name"`
	Remark    string       `json:"remark"`
	HostKey   *HostKeyView `json:"host_key"`
	CreatedAt string       `json:"created_at"`
	UpdatedAt string       `json:"updated_at"`
}
type Record struct {
	ID, Name, Host, Username, GroupName, Remark string
	Port                                        int
	PasswordEncrypted                           []byte
	PublicKey                                   []byte
	Algorithm, Fingerprint                      string
	CreatedAt, UpdatedAt                        int64
}

func Timestamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }
func (r Record) view() View {
	v := View{ID: r.ID, Name: r.Name, Host: r.Host, Port: r.Port, Username: r.Username, GroupName: r.GroupName, Remark: r.Remark, CreatedAt: Timestamp(time.UnixMilli(r.CreatedAt)), UpdatedAt: Timestamp(time.UnixMilli(r.UpdatedAt))}
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
	if !utf8.ValidString(in.Name+in.Host+in.Username+in.Password+in.GroupName+in.Remark) || len(in.Name) < 1 || len(in.Name) > 128 || len(in.Host) < 1 || len(in.Host) > 253 || *in.Port < 1 || *in.Port > 65535 || len(in.Username) < 1 || len(in.Username) > 128 || strings.ContainsRune(in.Username, 0) || len(in.Password) > 4096 || (create && in.Password == "") || len(in.GroupName) > 128 || len(in.Remark) > 1024 {
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
