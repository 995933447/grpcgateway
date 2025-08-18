package test

import (
	"net/url"
	"testing"

	"github.com/95933447/grpcgateway"
	"github.com/95933447/grpcgateway/test/client/testdecode"
	"google.golang.org/protobuf/types/dynamicpb"

	"google.golang.org/protobuf/proto"
)

func TestDecodePbFromURLValues(t *testing.T) {
	// 构建 url.Values 模拟请求
	values := url.Values{
		"string_val":                        {"hello"},
		"int32_val":                         {"123"},
		"bool_val":                          {"true"},
		"repeated_str[0]":                   {"first"},
		"repeated_str[1]":                   {"second"},
		"map_val.key1":                      {"value1"},
		"map_val.key2":                      {"value2"},
		"nested_msg.nested_int":             {"42"},
		"repeated_nested_msg[0].nested_int": {"66"},
		"repeated_nested_msg[1].nested_int": {"88"},
	}

	// 创建 dynamicpb.Message
	msg := dynamicpb.NewMessage((&testdecode.TestMessage{}).ProtoReflect().Descriptor())

	// 调用 DecodePbFromURLValues
	err := grpcgateway.DecodePbFromURLValues(msg, values)
	if err != nil {
		t.Fatalf("DecodePbFromURLValues failed: %v", err)
	}

	// 构建期望结果
	expected := &testdecode.TestMessage{
		StringVal:   "hello",
		Int32Val:    123,
		BoolVal:     true,
		RepeatedStr: []string{"first", "second"},
		MapVal: map[string]string{
			"key1": "value1",
			"key2": "value2",
		},
		NestedMsg: &testdecode.NestedMessage{
			NestedInt: 43,
		},
	}

	// 转为 proto.Message 进行比较
	if !proto.Equal(msg.Interface(), expected) {
		t.Errorf("decoded message not equal expected\nGot: %v\nWant: %v", msg.Interface(), expected)
	}

	t.Log(msg.Interface())
}
