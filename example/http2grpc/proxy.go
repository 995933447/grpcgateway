package main

import (
	"fmt"

	"github.com/95933447/grpcgateway"
	"github.com/95933447/grpcgateway/example/client/ext"
	"github.com/995933447/fastlog/logger"
	"github.com/jhump/protoreflect/desc"
	"github.com/valyala/fasthttp"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

func main() {
	err := grpcgateway.Init(&grpcgateway.Conf{
		ServiceName: "httpproxy",
		GrpcConf: grpcgateway.GrpcConf{
			MicroGoSuitConf: grpcgateway.MicroGoSuitConf{
				MicroGoSuitDiscoverPrefix:       "test_discovery",
				MicroGoSuitMetadataConfFilePath: "../meta.json",
			},
			GrpcResolveSchema: "testschema",
		},
		LogConf: grpcgateway.LogConf{
			Log: logger.LogConf{
				File: logger.FileLogConf{
					LogInfoBeforeFileSizeBytes:  -1,
					LogDebugBeforeFileSizeBytes: -1,
					Level:                       "DBG",
					DefaultLogDir:               "/var/work/logs/grpcgateway/example/echo/log",
					BillLogDir:                  "/var/work/logs/grpcgateway/example/echo/bill",
					StatLogDir:                  "/var/work/logs/grpcgateway/example/echo/stat",
				},
			},
		},
	})
	if err != nil {
		panic(err)
	}

	fmt.Println("running http2grpc-gateway.... listening at:127.0.0.1:8001")

	err = grpcgateway.HandleHttp("127.0.0.1", 8001, grpcgateway.ResolveRpcRouteFromHttp, func(ctx *fasthttp.RequestCtx, method *desc.MethodDescriptor) (interface{}, map[string][]string, []grpc.CallOption, error) {
		if !proto.HasExtension(method.GetMethodOptions(), ext.E_HttpProxyAccessRule) {
			return nil, nil, nil, grpcgateway.ErrNotSupportHttpAccess
		}

		opt := proto.GetExtension(method.GetMethodOptions(), ext.E_HttpProxyAccessRule)
		httpOpt, ok := opt.(*ext.HttpProxyAccessRule)
		if !ok {
			return nil, nil, nil, grpcgateway.ErrNotSupportHttpAccess
		}

		if httpOpt.Method != string(ctx.Method()) {
			return nil, nil, nil, grpcgateway.ErrNotSupportMethod
		}

		if !httpOpt.NoAuth {
			if token := ctx.Request.Header.Peek("token"); token == nil || string(token) != "123456" {
				return nil, nil, nil, grpcgateway.ErrNoAuth
			}
		}

		return grpcgateway.ResolveRpcParamsFromHttp(ctx, method)
	}, grpcgateway.RespHttp)
	if err != nil {
		panic(err)
	}
}
