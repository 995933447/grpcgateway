package test

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/url"
	"testing"

	"github.com/995933447/grpcgateway"
	"github.com/995933447/grpcgateway/test/client/testdecode"
	"google.golang.org/protobuf/types/dynamicpb"

	"google.golang.org/protobuf/proto"
)

func TestDecodePbFromURLValues(t *testing.T) {
	// 构建 url.Values 模拟请求
	// 数组用#1 或者 #2 形式表示都可以，但是不能同时存在，选其中一种形式即可，否则会出现意料不到的效果。
	// 比如这个例子，因为values是无序的，如果先写入key repeated_str[2]部分，repeat_str字段值为: []string{"", "", "third"},
	// 然后写入repeated_str部分，repeat_str字段值为: []string{"", "", "third", "first", "second"},
	// 最后写入key repeated_str[3]部分，repeat_str字段值为: []string{"", "", "third", "4th", "second"}, 所以可能导致first丢失
	values := url.Values{
		"string_val":                        {"hello"},
		"int32_val":                         {"123"},
		"bool_val":                          {"true"},
		"repeated_str":                      {"first", "second"}, // 两种方式都ok #1
		"repeated_str[2]":                   {"third"},           // 两种方式都ok #2
		"repeated_str[3]":                   {"4th"},             // 两种方式都ok #2
		"map_val.key1":                      {"value1"},
		"map_val.key2":                      {"value2"},
		"nested_msg.nested_int":             {"42"},
		"repeated_nested_msg[0].nested_int": {"66"},
		"repeated_nested_msg[1].nested_int": {"88"},
	}

	// 创建一个长度为 20 的字节切片
	b := make([]byte, 20)

	// 使用 rand.Read 填充随机字节
	_, err := rand.Read(b)

	bbase64 := base64.StdEncoding.EncodeToString(b)

	fmt.Println(b)
	fmt.Println(bbase64)

	values.Add("file_content", bbase64)
	values.Add("repeated_file", bbase64)
	values.Add("repeated_file", bbase64)

	// 创建 dynamicpb.Message
	msg := dynamicpb.NewMessage((&testdecode.TestMessage{}).ProtoReflect().Descriptor())

	// 调用 DecodePbFromURLValues
	err = grpcgateway.DecodePbFromURLValues(msg, values)
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
		FileContent: b,
	}

	// 转为 proto.Message 进行比较
	if !proto.Equal(msg.Interface(), expected) {
		t.Errorf("decoded message not equal expected\nGot: %v\nWant: %v", msg.Interface(), expected)
	}

	t.Log(msg.Interface())

	// 没问题 如何参数格式都解析成功
	//	=== RUN   TestDecodePbFromURLValues
	//	[191 251 35 1 6 101 139 68 107 56 183 52 82 138 156 152 98 7 121 252]
	//	v/sjAQZli0RrOLc0UoqcmGIHefw=
	//	decode_test.go:75: decoded message not equal expected
	//	Got: string_val:"hello"  int32_val:123  bool_val:true  repeated_str:""  repeated_str:""  repeated_str:"third"  repeated_str:"4th"  repeated_str:"second"  map_val:{key:"key1"  value:"value1"}  map_val:{key:"key2"  value:"value2"}  nested_msg:{nested_int:42}  repeated_nested_msg:{nested_int:66}  repeated_nested_msg:{nested_int:88}  file_content:"\xbf\xfb#\x01\x06e\x8bDk8\xb74R\x8a\x9c\x98b\x07y\xfc"  repeated_file:"\xbf\xfb#\x01\x06e\x8bDk8\xb74R\x8a\x9c\x98b\x07y\xfc"  repeated_file:"\xbf\xfb#\x01\x06e\x8bDk8\xb74R\x8a\x9c\x98b\x07y\xfc"
	//	Want: string_val:"hello"  int32_val:123  bool_val:true  repeated_str:"first"  repeated_str:"second"  map_val:{key:"key1"  value:"value1"}  map_val:{key:"key2"  value:"value2"}  nested_msg:{nested_int:43}  file_content:"\xbf\xfb#\x01\x06e\x8bDk8\xb74R\x8a\x9c\x98b\x07y\xfc"
	//	decode_test.go:78: string_val:"hello"  int32_val:123  bool_val:true  repeated_str:""  repeated_str:""  repeated_str:"third"  repeated_str:"4th"  repeated_str:"second"  map_val:{key:"key1"  value:"value1"}  map_val:{key:"key2"  value:"value2"}  nested_msg:{nested_int:42}  repeated_nested_msg:{nested_int:66}  repeated_nested_msg:{nested_int:88}  file_content:"\xbf\xfb#\x01\x06e\x8bDk8\xb74R\x8a\x9c\x98b\x07y\xfc"  repeated_file:"\xbf\xfb#\x01\x06e\x8bDk8\xb74R\x8a\x9c\x98b\x07y\xfc"  repeated_file:"\xbf\xfb#\x01\x06e\x8bDk8\xb74R\x8a\x9c\x98b\x07y\xfc"
	//	--- FAIL: TestDecodePbFromURLValues (0.00s)
}
