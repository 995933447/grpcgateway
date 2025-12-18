mkdir -p ./client/testdecode
protoc --go_out=./client/testdecode --go_opt=paths=source_relative --proto_path=./proto ./proto/test_decode.proto
