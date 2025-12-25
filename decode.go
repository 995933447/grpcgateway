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
			nodePath, err := parsePathV2(key)
			if err != nil {
				return err
			}

			if err := setFieldV2(msg, nodePath, v); err != nil {
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

// var pathRe = regexp.MustCompile(`([^.[]+)(\[(\d+)\])?`)
var pathRe = regexp.MustCompile(`([^.[]+)(?:\[(\d+|[^\]]+)\])?`)

type pathElem struct {
	field string
	index *int
	key   *string // 新增：map key / message field
}

func parsePath(s string) []pathElem {
	parts := strings.Split(s, ".")
	var res []pathElem

	for _, p := range parts {
		m := pathRe.FindStringSubmatch(p)
		if len(m) == 0 {
			continue
		}

		elem := pathElem{field: m[1]}

		if m[2] != "" {
			// 尝试解析成数字 index
			if idx, err := strconv.Atoi(m[2]); err == nil {
				elem.index = &idx
			} else {
				key := m[2]
				elem.key = &key
			}
		}

		res = append(res, elem)
	}
	return res
}

func setField(msg *dynamicpb.Message, path []pathElem, raw string) error {
	if len(path) == 0 {
		return nil
	}

	elem := path[0]
	fd := msg.Descriptor().Fields().ByName(protoreflect.Name(elem.field))
	if fd == nil {
		return nil // unknown field -> ignore (grpc-gateway 行为)
	}

	// ---------------- map ----------------
	if fd.IsMap() {
		mp := msg.Mutable(fd).Map()

		if elem.key == nil {
			return fmt.Errorf("map field %s requires key", elem.field)
		}

		mapKey, err := parseMapKey(fd.MapKey(), *elem.key)
		if err != nil {
			return err
		}

		valDesc := fd.MapValue()

		// map value is message → recurse
		if valDesc.Kind() == protoreflect.MessageKind {
			subMsg := mp.Get(mapKey).Message()
			if !subMsg.IsValid() {
				subMsg = dynamicpb.NewMessage(valDesc.Message()).ProtoReflect()
				mp.Set(mapKey, protoreflect.ValueOfMessage(subMsg))
			}
			return setField(
				subMsg.Interface().(*dynamicpb.Message),
				path[1:],
				raw,
			)
		}

		// scalar map value
		val, err := parseValue(valDesc, raw)
		if err != nil {
			return err
		}
		mp.Set(mapKey, val)
		return nil
	}

	// ---------------- message ----------------
	if fd.Kind() == protoreflect.MessageKind {
		// repeated message
		if fd.IsList() {
			list := msg.Mutable(fd).List()
			idx := 0
			if elem.index != nil {
				idx = *elem.index
			}
			for list.Len() <= idx {
				list.Append(
					protoreflect.ValueOfMessage(
						dynamicpb.NewMessage(fd.Message()),
					),
				)
			}
			sub := list.Get(idx).Message().Interface().(*dynamicpb.Message)
			return setField(sub, path[1:], raw)
		}

		// single message
		sub := msg.Mutable(fd).Message().Interface().(*dynamicpb.Message)

		// ⭐ 核心：page[page_size] 走这里
		if elem.key != nil {
			return setField(
				sub,
				append([]pathElem{{field: *elem.key}}, path[1:]...),
				raw,
			)
		}

		return setField(sub, path[1:], raw)
	}

	// 基础类型
	val, err := parseValue(fd, raw)
	if err != nil {
		return err
	}

	if fd.IsList() {
		list := msg.Mutable(fd).List()
		if path[0].index != nil {
			idx := *path[0].index
			for list.Len() <= idx {
				list.Append(zeroValue(fd))
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

type PathNodeKind int

const (
	PathField PathNodeKind = iota
	PathIndex
)

type PathNode struct {
	Kind  PathNodeKind
	Name  string // field / map key
	Index int    // repeated index
}

func parsePathV2(s string) ([]PathNode, error) {
	var res []PathNode
	i := 0
	n := len(s)

	readIdent := func() string {
		start := i
		for i < n {
			c := s[i]
			if c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
				i++
			} else {
				break
			}
		}
		return s[start:i]
	}

	for i < n {
		switch s[i] {
		case '.':
			i++
		case '[':
			i++ // skip '['
			inner, err := parsePathV2(s[i:])
			if err != nil {
				return nil, err
			}
			res = append(res, inner...)
			// 跳到匹配的 ']'
			brackets := 1
			for i < n && brackets > 0 {
				if s[i] == '[' {
					brackets++
				} else if s[i] == ']' {
					brackets--
				}
				i++
			}
		case ']':
			return res, nil
		default:
			ident := readIdent()
			if ident == "" {
				return nil, fmt.Errorf("invalid path at %d", i)
			}
			if idx, err := strconv.Atoi(ident); err == nil {
				res = append(res, PathNode{Kind: PathIndex, Index: idx})
			} else {
				res = append(res, PathNode{Kind: PathField, Name: ident})
			}
		}
	}
	return res, nil
}

func setFieldV2(msg *dynamicpb.Message, path []PathNode, raw string) error {
	if len(path) == 0 {
		return nil
	}

	node := path[0]

	if node.Kind != PathField {
		return fmt.Errorf("path must start with field")
	}

	fd := msg.Descriptor().Fields().ByName(protoreflect.Name(node.Name))
	if fd == nil {
		return nil // unknown field → ignore (grpc-gateway behavior)
	}

	return setFieldValueByNodePath(msg, fd, path[1:], raw)
}

func setFieldValueByNodePath(msg *dynamicpb.Message, fd protoreflect.FieldDescriptor, path []PathNode, raw string) error {

	// ===== MAP =====
	if fd.IsMap() {
		if len(path) == 0 || path[0].Kind != PathField {
			return fmt.Errorf("map field requires key")
		}

		keyNode := path[0]
		key, err := parseMapKey(fd.MapKey(), keyNode.Name)
		if err != nil {
			return err
		}

		valDesc := fd.MapValue()
		mp := msg.Mutable(fd).Map()

		// map value 是 message
		if valDesc.Kind() == protoreflect.MessageKind {
			sub := mp.Get(key)
			var subMsg *dynamicpb.Message
			if !sub.IsValid() {
				subMsg = dynamicpb.NewMessage(valDesc.Message())
				mp.Set(key, protoreflect.ValueOfMessage(subMsg))
			} else {
				subMsg = sub.Message().Interface().(*dynamicpb.Message)
			}
			return setFieldV2(subMsg, path[1:], raw)
		}

		// map value 是 scalar
		val, err := parseValue(valDesc, raw)
		if err != nil {
			return err
		}
		mp.Set(key, val)
		return nil
	}

	// ===== MESSAGE =====
	if fd.Kind() == protoreflect.MessageKind {
		// repeated message
		if fd.IsList() {
			if len(path) == 0 || path[0].Kind != PathIndex {
				return fmt.Errorf("repeated message requires index")
			}
			idx := path[0].Index
			list := msg.Mutable(fd).List()

			for list.Len() <= idx {
				list.Append(protoreflect.ValueOfMessage(
					dynamicpb.NewMessage(fd.Message()),
				))
			}

			sub := list.Get(idx).Message().Interface().(*dynamicpb.Message)
			return setFieldV2(sub, path[1:], raw)
		}

		// single message
		sub := msg.Mutable(fd).Message().Interface().(*dynamicpb.Message)
		return setFieldV2(sub, path, raw)
	}

	// ===== SCALAR =====
	val, err := parseValue(fd, raw)
	if err != nil {
		return err
	}

	if fd.IsList() {
		// repeated scalar
		if len(path) > 0 && path[0].Kind == PathIndex {
			idx := path[0].Index
			list := msg.Mutable(fd).List()
			for list.Len() <= idx {
				list.Append(zeroValue(fd))
			}
			list.Set(idx, val)
		} else {
			msg.Mutable(fd).List().Append(val)
		}
		return nil
	}

	msg.Set(fd, val)
	return nil
}

func zeroValue(fd protoreflect.FieldDescriptor) protoreflect.Value {
	switch fd.Kind() {
	case protoreflect.BoolKind:
		return protoreflect.ValueOfBool(false)
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		return protoreflect.ValueOfInt32(0)
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return protoreflect.ValueOfInt64(0)
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return protoreflect.ValueOfUint32(0)
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return protoreflect.ValueOfUint64(0)
	case protoreflect.FloatKind:
		return protoreflect.ValueOfFloat32(0)
	case protoreflect.DoubleKind:
		return protoreflect.ValueOfFloat64(0)
	case protoreflect.StringKind:
		return protoreflect.ValueOfString("")
	case protoreflect.BytesKind:
		return protoreflect.ValueOfBytes(nil)
	case protoreflect.EnumKind:
		return protoreflect.ValueOfEnum(0)
	default:
		return protoreflect.Value{}
	}
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
