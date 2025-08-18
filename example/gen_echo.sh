mkdir -p ./client/echo
protoc --go_out=. --go-grpc_out=. --proto_path=./proto ./proto/echo.proto
