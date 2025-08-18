package grpcgateway

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"encoding/base64"
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

// DecodePbFromJson json → dynamicpb.Message 
func DecodePbFromJson(msg *dynamicpb.Message, v []byte) error {
	opt := protojson.UnmarshalOptions{
		AllowPartial:   true,
		DiscardUnknown: true,
	}

	err := opt.Unmarshal(v, msg)
	if err != nil {
		return err
	}

	return nil
}

// DecodePbFromURLValues FromURLValues url.Values → dynamicpb.Message 已完成支持所有类型的参数
func DecodePbFromURLValues(msg *dynamicpb.Message, values url.Values) error {
	for key, vals := range values {
		for _, v := range vals {
			if err := setField(msg, parsePath(key), v); err != nil {
				return fmt.Errorf("set field %s failed: %w", key, err)
			}
		}
	}

	return nil
}

// 支持基础类型解析
func parseValue(fd protoreflect.FieldDescriptor, raw string) (protoreflect.Value, error) {
	switch fd.Kind() {
	case protoreflect.StringKind:
		return protoreflect.ValueOfString(raw), nil
	case protoreflect.BytesKind:
		b, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return protoreflect.Value{}, err
		}
		return protoreflect.ValueOfBytes(b), nil
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		i, err := strconv.ParseInt(raw, 10, 32)
		if err != nil {
			return protoreflect.Value{}, err
		}
		return protoreflect.ValueOfInt32(int32(i)), nil
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		i, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return protoreflect.Value{}, err
		}
		return protoreflect.ValueOfInt64(i), nil
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		i, err := strconv.ParseUint(raw, 10, 32)
		if err != nil {
			return protoreflect.Value{}, err
		}
		return protoreflect.ValueOfUint32(uint32(i)), nil
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		i, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return protoreflect.Value{}, err
		}
		return protoreflect.ValueOfUint64(i), nil
	case protoreflect.BoolKind:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return protoreflect.Value{}, err
		}
		return protoreflect.ValueOfBool(b), nil
	case protoreflect.FloatKind:
		f, err := strconv.ParseFloat(raw, 32)
		if err != nil {
			return protoreflect.Value{}, err
		}
		return protoreflect.ValueOfFloat32(float32(f)), nil
	case protoreflect.DoubleKind:
		f, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return protoreflect.Value{}, err
		}
		return protoreflect.ValueOfFloat64(f), nil
	case protoreflect.EnumKind:
		ed := fd.Enum()
		val := ed.Values().ByName(protoreflect.Name(raw))
		if val != nil {
			return protoreflect.ValueOfEnum(val.Number()), nil
		}
		// 尝试数字
		i, err := strconv.Atoi(raw)
		if err != nil {
			return protoreflect.Value{}, err
		}
		return protoreflect.ValueOfEnum(protoreflect.EnumNumber(i)), nil
	default:
		return protoreflect.Value{}, fmt.Errorf("unsupported kind: %s", fd.Kind())
	}
}

var pathRe = regexp.MustCompile(`([^.[]+)(\[(\d+)\])?`)

type pathElem struct {
	field string
	index *int // repeated 下标（nil 表示无下标）
}

func parsePath(s string) []pathElem {
	parts := strings.Split(s, ".")
	var res []pathElem
	for _, p := range parts {
		m := pathRe.FindStringSubmatch(p)
		if len(m) > 0 {
			if m[3] != "" {
				idx, _ := strconv.Atoi(m[3])
				res = append(res, pathElem{field: m[1], index: &idx})
			} else {
				res = append(res, pathElem{field: m[1]})
			}
		}
	}
	return res
}

func setField(msg *dynamicpb.Message, path []pathElem, raw string) error {
	if len(path) == 0 {
		return nil
	}

	fd := msg.Descriptor().Fields().ByName(protoreflect.Name(path[0].field))
	if fd == nil {
		return nil // 不存在的字段，跳过
	}

	if fd.IsMap() {
		// map[K]T
		mp := msg.Mutable(fd).Map()
		valDesc := fd.MapValue()
		keyDesc := fd.MapKey()
		// key 使用 path[1].field
		if len(path) < 2 {
			return fmt.Errorf("map field requires key in path")
		}

		mapKey, err := parseMapKey(keyDesc, path[1].field)
		if err != nil {
			return err
		}

		val, err := parseValue(valDesc, raw)
		if err != nil {
			return err
		}

		mp.Set(mapKey, val)
		return nil
	}

	if fd.Kind() == protoreflect.MessageKind {
		if fd.IsList() {
			// repeated message
			list := msg.Mutable(fd).List()
			idx := 0
			if path[0].index != nil {
				idx = *path[0].index
			}
			for list.Len() <= idx {
				list.Append(protoreflect.ValueOfMessage(dynamicpb.NewMessage(fd.Message())))
			}
			subMsg := list.Get(idx).Message().Interface().(*dynamicpb.Message)
			return setField(subMsg, path[1:], raw)
		} else {
			// 单值 message
			subMsg := msg.Mutable(fd).Message().Interface().(*dynamicpb.Message)
			return setField(subMsg, path[1:], raw)
		}
	}

	// 基础类型
	val, err := parseValue(fd, raw)
	if err != nil {
		return err
	}

	if fd.IsList() {
		list := msg.Mutable(fd).List()
		if path[0].index != nil {
			var zero protoreflect.Value
			switch fd.Kind() {
			case protoreflect.BoolKind:
				zero = protoreflect.ValueOfBool(false)
			case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
				zero = protoreflect.ValueOfInt32(0)
			case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
				zero = protoreflect.ValueOfInt64(0)
			case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
				zero = protoreflect.ValueOfUint32(0)
			case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
				zero = protoreflect.ValueOfUint64(0)
			case protoreflect.FloatKind:
				zero = protoreflect.ValueOfFloat32(0)
			case protoreflect.DoubleKind:
				zero = protoreflect.ValueOfFloat64(0)
			case protoreflect.StringKind:
				zero = protoreflect.ValueOfString("")
			case protoreflect.BytesKind:
				zero = protoreflect.ValueOfBytes(nil)
			case protoreflect.EnumKind:
				zero = protoreflect.ValueOfEnum(0)
			}
			idx := *path[0].index
			for list.Len() <= idx {
				list.Append(zero)
			}
			list.Set(idx, val)
		} else {
			list.Append(val)
		}
	} else {
		msg.Set(fd, val)
	}
	return nil
}

func parseMapKey(fd protoreflect.FieldDescriptor, raw string) (protoreflect.MapKey, error) {
	switch fd.Kind() {
	case protoreflect.StringKind:
		return protoreflect.ValueOfString(raw).MapKey(), nil
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		i, err := strconv.ParseInt(raw, 10, 32)
		if err != nil {
			return protoreflect.MapKey{}, err
		}
		return protoreflect.ValueOfInt32(int32(i)).MapKey(), nil
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		i, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return protoreflect.MapKey{}, err
		}
		return protoreflect.ValueOfInt64(i).MapKey(), nil
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		i, err := strconv.ParseUint(raw, 10, 32)
		if err != nil {
			return protoreflect.MapKey{}, err
		}
		return protoreflect.ValueOfUint32(uint32(i)).MapKey(), nil
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		i, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return protoreflect.MapKey{}, err
		}
		return protoreflect.ValueOfUint64(i).MapKey(), nil
	case protoreflect.BoolKind:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return protoreflect.MapKey{}, err
		}
		return protoreflect.ValueOfBool(b).MapKey(), nil
	default:
		return protoreflect.MapKey{}, fmt.Errorf("unsupported map key kind: %s", fd.Kind())
	}
}
