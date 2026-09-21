// Package jsonx decodes untrusted JSON without ambiguous duplicate keys or null primitives.
package jsonx

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
)

func Decode(raw []byte, dst any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := scan(d); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return fmt.Errorf("JSONの後に余分なデータがあります")
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return fmt.Errorf("JSONの形式が不正です")
	}
	if err := check(value, reflect.TypeOf(dst).Elem()); err != nil {
		return err
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return fmt.Errorf("項目名または値の型が不正です")
	}
	return nil
}

func scan(d *json.Decoder) error {
	t, err := d.Token()
	if err != nil {
		return fmt.Errorf("JSONの形式が不正です")
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			k, err := d.Token()
			if err != nil {
				return fmt.Errorf("JSONの形式が不正です")
			}
			key, ok := k.(string)
			if !ok || seen[key] {
				return fmt.Errorf("JSONの項目名が重複しています")
			}
			seen[key] = true
			if err := scan(d); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := scan(d); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("JSONの形式が不正です")
	}
	_, err = d.Token()
	return err
}

var rawType = reflect.TypeOf(json.RawMessage{})

func check(v any, t reflect.Type) error {
	if t == rawType {
		return nil
	}
	if t.Kind() == reflect.Pointer {
		if v == nil {
			return nil
		}
		return check(v, t.Elem())
	}
	if v == nil {
		if t.Kind() == reflect.Slice {
			return nil
		}
		return fmt.Errorf("この項目にnullは指定できません")
	}
	switch t.Kind() {
	case reflect.Struct:
		obj, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("オブジェクトが必要です")
		}
		fields := map[string]reflect.Type{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			name := strings.Split(f.Tag.Get("json"), ",")[0]
			if name != "" && name != "-" {
				fields[name] = f.Type
			}
		}
		for name, value := range obj {
			ft, ok := fields[name]
			if !ok {
				return fmt.Errorf("許可されていない項目があります")
			}
			if err := check(value, ft); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		arr, ok := v.([]any)
		if !ok {
			return fmt.Errorf("配列が必要です")
		}
		for _, value := range arr {
			if err := check(value, t.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}

// Require checks presence and rejects null even where Go would silently use a zero value.
func Require(raw []byte, keys ...string) error {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil || obj == nil {
		return fmt.Errorf("オブジェクトが必要です")
	}
	for _, key := range keys {
		v, ok := obj[key]
		if !ok || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return fmt.Errorf("必須項目がありません")
		}
	}
	return nil
}
