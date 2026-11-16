package websocket

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"unicode/utf8"
)

type bindMessage struct {
	Type       string  `json:"type"`
	Credential string  `json:"credential"`
	Offset     *uint64 `json:"requested_start_offset"`
}
type resizeMessage struct {
	Type       string  `json:"type"`
	Cols       *int    `json:"cols"`
	Rows       *int    `json:"rows"`
	Generation *uint64 `json:"binding_generation"`
}

func decode(data []byte, dst any) error {
	if !utf8.Valid(data) {
		return errors.New("INVALID_MESSAGE")
	}
	// 标准 Unmarshal 接受重复键；协议必须在正式解码前显式拒绝。
	d := json.NewDecoder(bytes.NewReader(data))
	token, e := d.Token()
	if e != nil || token != json.Delim('{') {
		return errors.New("INVALID_MESSAGE")
	}
	seen := map[string]bool{}
	allowed := map[string]bool{}
	typ := reflect.TypeOf(dst).Elem()
	for i := 0; i < typ.NumField(); i++ {
		allowed[typ.Field(i).Tag.Get("json")] = true
	}
	for d.More() {
		key, e := d.Token()
		if e != nil {
			return e
		}
		name, ok := key.(string)
		if !ok || seen[name] || !allowed[name] {
			return errors.New("INVALID_MESSAGE")
		}
		seen[name] = true
		var raw json.RawMessage
		if e = d.Decode(&raw); e != nil {
			return e
		}
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return errors.New("INVALID_MESSAGE")
		}
	}
	if _, e = d.Token(); e != nil {
		return e
	}
	if _, e = d.Token(); e != io.EOF {
		return errors.New("INVALID_MESSAGE")
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	return d.Decode(dst)
}
func closeCode(reason string) int {
	switch reason {
	case "taken_over":
		return 4001
	case "closed":
		return 1000
	case "MESSAGE_TOO_LARGE":
		return 1009
	case "OUTPUT_GAP":
		return 4002
	case "SLOW_CLIENT":
		return 1013
	case "SERVER_SHUTTING_DOWN":
		return 1012
	case "INTERNAL_ERROR", "NETWORK_ERROR":
		return 1011
	default:
		return 1008
	}
}

// 服务端控制消息使用具名 DTO，字段集合与冻结协议一致。
type statusMessage struct {
	Type   string `json:"type"`
	Status string `json:"status"`
}
type bindSuccess struct {
	Type       string `json:"type"`
	Generation uint64 `json:"binding_generation"`
	Start      uint64 `json:"replay_start_offset"`
	End        uint64 `json:"replay_end_offset"`
	Cols       int    `json:"current_cols"`
	Rows       int    `json:"current_rows"`
}
type historyTruncated struct {
	Type      string `json:"type"`
	Requested uint64 `json:"requested_start_offset"`
	Start     uint64 `json:"replay_start_offset"`
	End       uint64 `json:"replay_end_offset"`
	Lost      uint64 `json:"lost_bytes"`
}
type errorMessage struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
}
type closedMessage struct {
	Type    string `json:"type"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

// taken_over 只从独立终止槽由唯一 Writer 发送。
type takenOverMessage struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}
