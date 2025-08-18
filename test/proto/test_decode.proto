syntax = "proto3";

package testdecode;

option go_package="client/testdecode";

message NestedMessage {
  int32 nested_int = 1;
}

message TestMessage {
  string string_val = 1;
  int32 int32_val = 2;
  bool bool_val = 3;
  repeated string repeated_str = 4;
  map<string, string> map_val = 5;
  NestedMessage nested_msg = 6;
  repeated NestedMessage repeated_nested_msg  = 7;
}
