mkdir -p ./client/ext
protoc --go_out=./client/ext --go-grpc_out=. --go_opt=paths=source_relative --proto_path=./proto ./proto/ext.proto
