package grpcgateway

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/995933447/fastlog"
	"github.com/995933447/stringhelper-go"
	"github.com/jhump/protoreflect/desc"
	json "github.com/json-iterator/go"
	"github.com/valyala/fasthttp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/dynamicpb"
)

func HandleHttpDefault(host string, port int) error {
	err := HandleHttp(host, port, ResolveRpcRouteFromHttp, ResolveRpcParamsFromHttp, RespHttp)
	if err != nil {
		fastlog.Errorf("err: %+v", err)
		return err
	}
	return nil
}

type ResolveRpcRouteFunc func(ctx *fasthttp.RequestCtx) (string, string, string, error)

type ResolveRpcParamsFunc func(ctx *fasthttp.RequestCtx, method *desc.MethodDescriptor) (interface{}, map[string][]string, []grpc.CallOption, error)

func HandleHttp(
	host string,
	port int,
	resolveRpcRouteFunc ResolveRpcRouteFunc,
	resolveRpcParamsFunc ResolveRpcParamsFunc,
	response func(res *ResponseHttp),
) error {
	defer ClearGrpcConns()

	err := fasthttp.ListenAndServe(fmt.Sprintf("%s:%d", host, port), func(ctx *fasthttp.RequestCtx) {
		packageName, svcName, methodName, err := resolveRpcRouteFunc(ctx)
		if err != nil {
			response(&ResponseHttp{
				Ctx:     ctx,
				Err:     err,
				Service: svcName,
				Method:  methodName,
			})
			return
		}

		rpcMetadataAny, ok := rpcMetadataMap.Load(fmt.Sprintf("%s.%s.%s", packageName, svcName, methodName))
		if !ok {
			response(&ResponseHttp{
				Ctx:     ctx,
				Err:     ErrServiceNotFound,
				Service: svcName,
				Method:  methodName,
			})
			return
		}

		rpcMeta := rpcMetadataAny.(*RpcMetadata)

		params, header, callOpts, err := resolveRpcParamsFunc(ctx, rpcMeta.method)
		if err != nil {
			response(&ResponseHttp{
				Ctx:       ctx,
				Err:       err,
				Service:   svcName,
				Method:    methodName,
				ReqHeader: header,
			})

			return
		}

		resp, respHeader, err := InvokeGrpc(packageName, svcName, methodName, params, header, callOpts)

		defer func() {
			if resp != nil {
				resp.Reset()
				rpcMeta.respPool.Put(resp)
			}
		}()

		respHttp := &ResponseHttp{
			Ctx:          ctx,
			Err:          err,
			Service:      svcName,
			Method:       methodName,
			RespMetadata: respHeader,
			ReqHeader:    header,
		}

		if resp != nil {
			respHttp.GrpcResp = resp
		}

		response(respHttp)

		return
	})
	if err != nil {
		fastlog.Errorf("err: %+v", err)
		return err
	}
	return nil
}

func ResolveRpcRouteFromHttp(ctx *fasthttp.RequestCtx) (string, string, string, error) {
	path := string(ctx.Path())
	if path == "" {
		return "", "", "", ErrServiceNotFound
	}

	pathComponents := strings.Split(path, "/")
	if len(pathComponents) < 3 {
		return "", "", "", ErrServiceNotFound
	}

	var packageName, svcName, methodName string
	if len(pathComponents) < 4 {
		svcName, methodName = pathComponents[1], pathComponents[2]
		packageName = svcName
	} else {
		packageName, svcName, methodName = pathComponents[1], pathComponents[2], pathComponents[3]
	}

	if packageName != "" {
		packageName = stringhelper.LowerFirstASCII(packageName)
	}

	return packageName, svcName, methodName, nil
}

func ResolveRpcParamsFromHttp(ctx *fasthttp.RequestCtx, _ *desc.MethodDescriptor) (interface{}, map[string][]string, []grpc.CallOption, error) {
	header := make(map[string][]string)
	ctx.Request.Header.VisitAll(func(key, value []byte) {
		k := strings.ToLower(string(key))
		// https://github.com/grpc/grpc-go/blob/master/internal/transport/http2_server.go#L417
		if k == "connection" {
			return
		}
		header[k] = []string{string(value)}
	})

	return HttpPramsToJsonOrUrlValues(ctx), header, nil, nil
}

func HttpPramsToJsonOrUrlValues(ctx *fasthttp.RequestCtx) interface{} {
	switch string(ctx.Method()) {
	case fasthttp.MethodPost, fasthttp.MethodPut, fasthttp.MethodPatch:
		// POST/PUT/PATCH → 尝试 JSON，否则解析 form
		contentType := string(ctx.Request.Header.ContentType())
		if contentType == "application/json" {
			// 直接返回 body
			return ctx.PostBody()
		}
	}

	values := url.Values{}

	// 1. GET/DELETE 查询参数（URL query）
	ctx.QueryArgs().VisitAll(func(key, value []byte) {
		values.Add(string(key), string(value))
	})

	// 2. POST/PUT/PATCH 表单参数（application/x-www-form-urlencoded）
	ctx.PostArgs().VisitAll(func(key, value []byte) {
		values.Add(string(key), string(value))
	})

	// 3. multipart/form-data
	if form, err := ctx.MultipartForm(); err == nil && form != nil {
		// 文本字段
		for key, vals := range form.Value {
			for _, v := range vals {
				values.Add(key, v)
			}
		}
		// 文件字段：转 base64
		for key, fileHeaders := range form.File {
			for _, fh := range fileHeaders {
				f, err := fh.Open()
				if err != nil {
					continue
				}
				data, err := io.ReadAll(f)
				f.Close()
				if err != nil {
					continue
				}
				encoded := base64.StdEncoding.EncodeToString(data)
				values.Add(key, encoded)
			}
		}
	}

	return values
}

func makeRpcReq(params interface{}, rpcMeta *RpcMetadata) (*dynamicpb.Message, error) {
	msg := rpcMeta.reqPool.Get().(*dynamicpb.Message)

	if params == nil {
		return msg, nil
	}

	switch v := params.(type) {
	case []byte:
		if err := DecodePbFromJson(msg, v); err != nil {
			return nil, err
		}
	case url.Values:
		if err := DecodePbFromURLValues(msg, v); err != nil {
			return nil, err
		}
	}

	return msg, nil
}

type GatewayResp struct {
	ErrCode int32       `json:"err_code"`
	ErrMsg  string      `json:"err_msg"`
	Data    interface{} `json:"data"`
}

type ResponseHttp struct {
	Ctx          *fasthttp.RequestCtx
	Err          error
	GrpcResp     proto.Message
	Method       string
	Service      string
	ReqHeader    map[string][]string
	RespMetadata metadata.MD
}

func RespHttp(res *ResponseHttp) {
	gatewayResp := &GatewayResp{}

	if res.Err != nil {
		if state, ok := status.FromError(res.Err); ok {
			gatewayResp.ErrCode = int32(state.Code())
			gatewayResp.ErrMsg = state.Message()
		} else {
			gatewayResp.ErrCode = -1
			gatewayResp.ErrMsg = res.Err.Error()
		}
	}

	if res.GrpcResp != nil {
		b, err := protojson.Marshal(res.GrpcResp)
		if err != nil {
			fastlog.Errorf("err: %+v", err)
			gatewayResp.ErrCode = -1
			gatewayResp.ErrMsg = res.Err.Error()
			return
		}

		m := make(map[string]interface{})
		if err = json.Unmarshal(b, &m); err != nil {
			fastlog.Errorf("err: %+v", err)
			gatewayResp.ErrCode = -1
			gatewayResp.ErrMsg = res.Err.Error()
			return
		}

		gatewayResp.Data = m
	}

	j, err := json.Marshal(gatewayResp)
	if err != nil {
		fastlog.Errorf("err:%v", err)
		return
	}

	res.Ctx.Response.Header.SetContentType("application/json")

	if _, err = fmt.Fprintf(res.Ctx, string(j)); err != nil {
		fastlog.Errorf("err:%v", err)
		return
	}
}

func getGrpcCtx(cfg *Conf, header map[string][]string) (context.Context, context.CancelFunc) {
	if cfg.CallClientTimeoutMs == 0 {
		return metadata.NewOutgoingContext(context.Background(), header), func() {}
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.CallClientTimeoutMs)*time.Millisecond)
	return metadata.NewOutgoingContext(ctx, header), cancel
}
