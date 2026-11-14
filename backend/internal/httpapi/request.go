package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"reflect"
	"strings"
)

// DecodeJSON 在读入前施加限额。请求 DTO 使用明确的 json 标签；omitempty 字段可省略。
// 本函数校验结构和类型，业务范围由具体请求 DTO 校验，不在这里创造业务规则。
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any, limit int64, allowEmpty bool) error {
	reader := http.MaxBytesReader(w, r.Body, limit)
	defer reader.Close()
	data, err := io.ReadAll(reader)
	if err != nil {
		var max *http.MaxBytesError
		if errors.As(err, &max) {
			return &Error{MessageTooLarge}
		}
		return &Error{InvalidRequest}
	}
	if len(bytes.TrimSpace(data)) == 0 {
		if allowEmpty {
			return nil
		}
		return &Error{InvalidRequest}
	}
	typ, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || typ != "application/json" {
		return &Error{InvalidRequest}
	}
	t := reflect.TypeOf(dst)
	if t == nil || t.Kind() != reflect.Pointer || reflect.ValueOf(dst).IsNil() || t.Elem().Kind() != reflect.Struct {
		return &Error{InternalError}
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := checkJSON(dec, t.Elem()); err != nil {
		return &Error{InvalidRequest}
	}
	if _, err := dec.Token(); err != io.EOF {
		return &Error{InvalidRequest}
	}
	if err := json.Unmarshal(data, dst); err != nil {
		return &Error{InvalidRequest}
	}
	return nil
}
func checkJSON(dec *json.Decoder, t reflect.Type) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if tok == nil {
		return errors.New("null 不允许")
	}
	switch t.Kind() {
	case reflect.Struct:
		if tok != json.Delim('{') {
			return errors.New("需要对象")
		}
		fields := map[string]reflect.StructField{}
		required := map[string]bool{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			parts := strings.Split(f.Tag.Get("json"), ",")
			if parts[0] == "" || parts[0] == "-" {
				continue
			}
			fields[parts[0]] = f
			required[parts[0]] = !strings.Contains(f.Tag.Get("json"), ",omitempty")
		}
		seen := map[string]bool{}
		for dec.More() {
			key, err := dec.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			f, exists := fields[name]
			if !ok || !exists || seen[name] {
				return errors.New("未知或重复字段")
			}
			seen[name] = true
			if err := checkJSON(dec, f.Type); err != nil {
				return err
			}
		}
		end, err := dec.Token()
		if err != nil || end != json.Delim('}') {
			return errors.New("对象未结束")
		}
		for name, need := range required {
			if need && !seen[name] {
				return errors.New("缺少必填字段")
			}
		}
	case reflect.String:
		if _, ok := tok.(string); !ok {
			return errors.New("需要字符串")
		}
	case reflect.Bool:
		if _, ok := tok.(bool); !ok {
			return errors.New("需要布尔值")
		}
	case reflect.Int, reflect.Int64, reflect.Int32, reflect.Uint, reflect.Uint64, reflect.Uint32:
		n, ok := tok.(json.Number)
		if !ok || strings.ContainsAny(n.String(), ".eE") {
			return errors.New("需要整数")
		}
	case reflect.Slice:
		if tok != json.Delim('[') {
			return errors.New("需要数组")
		}
		for dec.More() {
			if err := checkJSON(dec, t.Elem()); err != nil {
				return err
			}
		}
		end, err := dec.Token()
		if err != nil || end != json.Delim(']') {
			return errors.New("数组未结束")
		}
	default:
		return errors.New("不支持的 DTO 类型")
	}
	return nil
}
