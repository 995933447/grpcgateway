package grpcgateway

import (
	"context"
	"fmt"
	"io"
	"sync"

	fastlogger "github.com/995933447/fastlog/logger"
	"github.com/995933447/fastlog/logger/writer"
	"github.com/995933447/microgosuit"
	"github.com/995933447/microgosuit/discovery"
	"github.com/995933447/microgosuit/factory"
	"github.com/995933447/runtimeutil"
	"github.com/golang/protobuf/proto"
	"github.com/jhump/protoreflect/desc"
	"github.com/jhump/protoreflect/dynamic/grpcdynamic"
	"github.com/jhump/protoreflect/grpcreflect"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/dynamicpb"
)

var rpcMetadataMap sync.Map // target.method => rpcMetadata

var (
	rpcConns  = make(map[string]*grpc.ClientConn)
	rpcConnMu sync.RWMutex
)

type RpcMetadata struct {
	svcName               string
	svcFullyQualifiedName string
	method                *desc.MethodDescriptor
	invokeMethodName      string
	reqPool               *sync.Pool
	respPool              *sync.Pool
}

func (r *RpcMetadata) GetServiceName() string {
	return r.svcName
}

func (r *RpcMetadata) GetServiceFullyQualifiedName() string {
	return r.svcFullyQualifiedName
}

func (r *RpcMetadata) GetMethod() *desc.MethodDescriptor {
	return r.method
}

func (r *RpcMetadata) GetInvokeMethodName() string {
	return r.invokeMethodName
}

type Conf struct {
	ServiceName string
	GrpcConf
}

type GrpcConf struct {
	MicroGoSuitConf
	GrpcResolveSchema                  string
	GrpcClientOptions                  []grpc.DialOption
	CallClientTimeoutMs                int64
	InitGrpcResolverFunc               func() error
	InitAndWatchGrpcClientMetadataFunc func(resolve func(svcHost string, svcPort int) error) error
}

type MicroGoSuitConf struct {
	MicroGoSuitMetadataConfFilePath string
	MicroGoSuitDiscoverPrefix       string
}

type LogConf struct {
	Log          fastlogger.LogConf
	LogAlertFunc writer.AlertFunc
}

var cfg *Conf

func Init(c *Conf) error {
	if err := checkConf(c); err != nil {
		return err
	}

	cfg = c

	if err := initGrpcResolver(); err != nil {
		return err
	}

	if err := initRpcWatcher(); err != nil {
		return err
	}

	return nil
}

func checkConf(cfg *Conf) error {
	if cfg.ServiceName == "" {
		return fmt.Errorf("config.ServiceName is empty")
	}

	if cfg.InitGrpcResolverFunc == nil && cfg.MicroGoSuitConf.MicroGoSuitMetadataConfFilePath == "" {
		return fmt.Errorf("must set config.InitGrpcResolverFunc or config.MicroGoSuitMetadataConfFilePath")
	}

	if cfg.InitAndWatchGrpcClientMetadataFunc == nil && cfg.MicroGoSuitConf.MicroGoSuitMetadataConfFilePath == "" {
		return fmt.Errorf("must set config.InitGrpcResolverFunc or config.MicroGoSuitMetadataConfFilePath")
	}

	if cfg.GrpcResolveSchema == "" {
		return fmt.Errorf("config.GrpcResolveSchema is empty")
	}

	return nil
}

func initGrpcResolver() error {
	if cfg.InitGrpcResolverFunc != nil {
		if err := cfg.InitGrpcResolverFunc(); err != nil {
			return err
		}
		return nil
	}

	err := microgosuit.InitSuitWithGrpc(context.TODO(), cfg.MicroGoSuitMetadataConfFilePath, cfg.GrpcResolveSchema, cfg.MicroGoSuitDiscoverPrefix)
	if err != nil {
		logger.Errorf("grpc resolver init err: %v", err)
		return err
	}

	return nil
}

// InvokeGrpcSupportStream grpcdynamic包实现版本
func InvokeGrpcSupportStream(packageName, svcName, methodName string, params interface{}, paramsCh chan interface{}, header map[string][]string, callOpts []grpc.CallOption) (metadata.MD, proto.Message, chan proto.Message, chan error, error) {
	rpcMetadataAny, ok := rpcMetadataMap.Load(fmt.Sprintf("%s.%s.%s", packageName, svcName, methodName))
	if !ok {
		return nil, nil, nil, nil, ErrServiceNotFound
	}

	rpcMeta := rpcMetadataAny.(*RpcMetadata)

	// context
	c, cancel := getGrpcCtx(cfg, header)

	conn, err := makeRpcConn(packageName, svcName)
	if err != nil {
		cancel()
		logger.Errorf("err:%v", err)
		return nil, nil, nil, nil, err
	}

	stub := grpcdynamic.NewStub(conn)

	switch {
	case rpcMeta.method.IsClientStreaming() && rpcMeta.method.IsServerStreaming(): // bidi
		respCh := make(chan proto.Message)
		errCh := make(chan error)

		stream, err := stub.InvokeRpcBidiStream(c, rpcMeta.method, callOpts...)
		if err != nil {
			cancel()
			return nil, nil, nil, nil, err
		}

		respHeader, _ := stream.Header()

		// 发送 goroutine
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()

			if paramsCh != nil {
				for m := range paramsCh {
					req, err := makeRpcReq(m, rpcMeta)
					if err != nil {
						errCh <- err
						continue
					}

					if err := stream.SendMsg(req); err != nil {
						errCh <- err
						continue
					}

					req.Reset()
					rpcMeta.reqPool.Put(req)
				}
			}

			err = stream.CloseSend()
			if err != nil {
				logger.Errorf("err:%v", err)
				errCh <- err
			}
		}()

		// 接收 goroutine（不要 close 外部 respCh，这里决定由调用者负责）
		go func() {
			defer wg.Done()

			for {
				m, err := stream.RecvMsg()
				if err == io.EOF {
					// 正常结束，返回
					return
				}

				if err != nil {
					errCh <- err
					continue
				}

				// 将收到的 proto.Message 发给 caller 提供的通道
				respCh <- m
			}
		}()

		go func() {
			wg.Wait()
			cancel()
			close(respCh)
			close(errCh)
		}()

		return respHeader, nil, respCh, errCh, nil
	case rpcMeta.method.IsServerStreaming():
		respCh := make(chan proto.Message)
		errCh := make(chan error)

		// 对于 server streaming，grpc.Header call option 对流不可用，应该在 stream 上调用 Header()
		req, err := makeRpcReq(params, rpcMeta)
		if err != nil {
			cancel()
			return nil, nil, nil, nil, err
		}

		defer func() {
			req.Reset()
			rpcMeta.reqPool.Put(req)
		}()

		stream, err := stub.InvokeRpcServerStream(c, rpcMeta.method, req, callOpts...)
		if err != nil {
			cancel()
			return nil, nil, nil, nil, err
		}

		// 在接收第一个消息之前或之后，你可以调用 stream.Header() 获取 header（如果服务端发了）
		respHeader, _ := stream.Header()

		go func() {
			defer func() {
				cancel()
				close(respCh)
				close(errCh)
			}()

			for {
				m, err := stream.RecvMsg()
				if err == io.EOF {
					// 正常结束
					break
				}

				if err != nil {
					errCh <- err
					continue
				}

				respCh <- m
			}
		}()

		return respHeader, nil, respCh, errCh, nil
	case rpcMeta.method.IsClientStreaming():
		stream, err := stub.InvokeRpcClientStream(c, rpcMeta.method, callOpts...)
		if err != nil {
			cancel()
			return nil, nil, nil, nil, err
		}

		respHeader, _ := stream.Header()

		errCh := make(chan error)
		respCh := make(chan proto.Message)
		go func() {
			defer cancel()

			if paramsCh != nil {
				// 发送所有请求，使用 range 避免重复 close
				for m := range paramsCh {
					req, err := makeRpcReq(m, rpcMeta)
					if err != nil {
						errCh <- err
						continue
					}

					if err := stream.SendMsg(req); err != nil {
						errCh <- err
						continue
					}

					req.Reset()
					rpcMeta.reqPool.Put(req)
				}
			}

			// 发送完成，关闭发送并接收单一响应
			resp, err := stream.CloseAndReceive()
			if err != nil {
				errCh <- err
				return
			}

			respCh <- resp
		}()

		return respHeader, nil, respCh, errCh, nil
	default:
		defer cancel()

		// 只有 unary 情况我们才能添加 HeaderCallOption
		respHeader := metadata.New(make(map[string]string))
		var existsRespHeader bool
		for _, opt := range callOpts {
			switch opt.(type) {
			case grpc.HeaderCallOption:
				respHeader = *opt.(grpc.HeaderCallOption).HeaderAddr
				existsRespHeader = true
			}
			if existsRespHeader {
				break
			}
		}
		if !existsRespHeader {
			callOpts = append(callOpts, grpc.Header(&respHeader))
		}

		req, err := makeRpcReq(params, rpcMeta)
		if err != nil {
			return nil, nil, nil, nil, err
		}

		defer func() {
			req.Reset()
			rpcMeta.reqPool.Put(req)
		}()

		resp, err := stub.InvokeRpc(c, rpcMeta.method, req, callOpts...)
		if err != nil {
			return nil, nil, nil, nil, err
		}

		return respHeader, resp, nil, nil, nil
	}
}

// InvokeGrpcSupportStreamV2 原生grpc实现版本
func InvokeGrpcSupportStreamV2(packageName, svcName, methodName string, params interface{}, paramsCh chan interface{}, header map[string][]string, callOpts []grpc.CallOption) (metadata.MD, *dynamicpb.Message, chan *dynamicpb.Message, chan error, error) {
	rpcMetadataAny, ok := rpcMetadataMap.Load(fmt.Sprintf("%s.%s.%s", packageName, svcName, methodName))
	if !ok {
		return nil, nil, nil, nil, ErrServiceNotFound
	}

	rpcMeta := rpcMetadataAny.(*RpcMetadata)

	desc := &grpc.StreamDesc{
		StreamName:    rpcMeta.invokeMethodName,
		ServerStreams: rpcMeta.method.IsServerStreaming(),
		ClientStreams: rpcMeta.method.IsClientStreaming(),
	}

	// context
	c, cancel := getGrpcCtx(cfg, header)

	conn, err := makeRpcConn(packageName, svcName)
	if err != nil {
		cancel()
		return nil, nil, nil, nil, err
	}

	respHeader := metadata.New(make(map[string]string))
	if !rpcMeta.method.IsClientStreaming() && !rpcMeta.method.IsServerStreaming() {
		var existsRespHeader bool
		for _, opt := range callOpts {
			switch opt.(type) {
			case grpc.HeaderCallOption:
				respHeader = *opt.(grpc.HeaderCallOption).HeaderAddr
				existsRespHeader = true
			}
			if existsRespHeader {
				break
			}
		}
		if !existsRespHeader {
			callOpts = append(callOpts, grpc.Header(&respHeader))
		}
	}

	stream, err := conn.NewStream(c, desc, rpcMeta.invokeMethodName, callOpts...)
	if err != nil {
		cancel()
		return respHeader, nil, nil, nil, err
	}

	respCh := make(chan *dynamicpb.Message)
	errCh := make(chan error)
	// 如果是客户端流或双向流 -> 可以多次发送
	if rpcMeta.method.IsClientStreaming() {
		respHeader, err = stream.Header()
		if err != nil {
			return nil, nil, nil, nil, err
		}

		go func() {
			if paramsCh != nil {
				for r := range paramsCh {
					req, err := makeRpcReq(r, rpcMeta)
					if err != nil {
						errCh <- err
						continue
					}

					if err := stream.SendMsg(req); err != nil {
						errCh <- err
					}

					req.Reset()
					rpcMeta.reqPool.Put(req)
				}
			}

			if err = stream.CloseSend(); err != nil {
				errCh <- err
			}
		}()
	} else {
		defer func() {
			err = stream.CloseSend()
			cancel()
		}()

		req, err := makeRpcReq(params, rpcMeta)
		if err != nil {
			return respHeader, nil, nil, nil, err
		}

		if err := stream.SendMsg(req); err != nil {
			return respHeader, nil, nil, nil, err
		}

		resp := rpcMeta.respPool.Get().(*dynamicpb.Message)
		err = stream.RecvMsg(resp)
		if err != nil {
			return respHeader, resp, nil, nil, err
		}

		return respHeader, resp, nil, nil, nil
	}

	go func() {
		defer func() {
			cancel()
			close(respCh)
			close(errCh)
		}()
		for {
			resp := rpcMeta.respPool.Get().(*dynamicpb.Message)
			err := stream.RecvMsg(resp)
			if err == io.EOF {
				break
			}
			if err != nil {
				errCh <- err
				continue
			}
			respCh <- resp
		}
	}()

	return respHeader, nil, respCh, errCh, nil
}

func InvokeGrpc(packageName, svcName, methodName string, params interface{}, header map[string][]string, callOpts []grpc.CallOption) (*dynamicpb.Message, metadata.MD, error) {
	rpcMetadataAny, ok := rpcMetadataMap.Load(fmt.Sprintf("%s.%s.%s", packageName, svcName, methodName))
	if !ok {
		return nil, nil, ErrServiceNotFound
	}

	rpcMeta := rpcMetadataAny.(*RpcMetadata)

	if rpcMeta.method.IsServerStreaming() || rpcMeta.method.IsClientStreaming() {
		return nil, nil, ErrNotSupportHttpAccess
	}

	c, cancel := getGrpcCtx(cfg, header)
	defer cancel()

	req, err := makeRpcReq(params, rpcMeta)
	if err != nil {
		return nil, nil, err
	}

	defer func() {
		req.Reset()
		rpcMeta.reqPool.Put(req)
	}()

	conn, err := makeRpcConn(packageName, svcName)
	if err != nil {
		logger.Errorf("err:%v", err)
		return nil, nil, err
	}

	respHeader := metadata.New(make(map[string]string))
	var existsRespHeader bool
	for _, opt := range callOpts {
		switch opt.(type) {
		case grpc.HeaderCallOption:
			respHeader = *opt.(grpc.HeaderCallOption).HeaderAddr
			existsRespHeader = true
		}
		if existsRespHeader {
			break
		}
	}
	if !existsRespHeader {
		callOpts = append(callOpts, grpc.Header(&respHeader))
	}
	//resp, err := grpcdynamic.NewStub(conn).InvokeRpc(c, rpcMeta.method, req, callOpts...)
	resp := rpcMeta.respPool.Get().(*dynamicpb.Message)
	err = conn.Invoke(c, rpcMeta.invokeMethodName, req, resp, callOpts...)

	return resp, respHeader, err
}

func makeRpcConn(packageName, svcName string) (*grpc.ClientConn, error) {
	target := fmt.Sprintf("%s:///%s.%s", cfg.GrpcResolveSchema, packageName, svcName)
	rpcConnMu.RLock()
	conn, ok := rpcConns[target]
	rpcConnMu.RUnlock()
	if !ok {
		rpcConnMu.Lock()
		defer rpcConnMu.Unlock()
		conn, ok = rpcConns[target]
		if !ok {
			var err error
			conn, err = grpc.NewClient(fmt.Sprintf("%s:///%s.%s", cfg.GrpcResolveSchema, packageName, svcName), GetGrpcClientOptions()...)
			if err != nil {
				logger.Errorf("err:%v", err)
				return nil, err
			}

			rpcConns[target] = conn
		}
	}

	return conn, nil
}

func initRpcWatcher() error {
	if cfg.InitAndWatchGrpcClientMetadataFunc != nil {
		return cfg.InitAndWatchGrpcClientMetadataFunc(resolve)
	}

	disc, err := factory.GetOrMakeDiscovery(cfg.MicroGoSuitDiscoverPrefix)
	if err != nil {
		return err
	}

	disc.OnSrvUpdated(func(ctx context.Context, evt discovery.Evt, svc *discovery.Service) {
		switch evt {
		case discovery.EvtUpdated:
			leastNode := svc.Nodes[len(svc.Nodes)-1]
			if err = resolve(leastNode.Host, leastNode.Port); err != nil {
				logger.Errorf("err:%v", err)
			}
		case discovery.EvtDeleted:
			var rmKeys []string
			rpcMetadataMap.Range(func(key, value interface{}) bool {
				if v, ok := value.(*RpcMetadata); ok && v.svcFullyQualifiedName == svc.SrvName {
					rmKeys = append(rmKeys, key.(string))
				}
				return true
			})
			for _, key := range rmKeys {
				rpcMetadataMap.Delete(key)
			}
		default:
		}
	})

	svcs, err := disc.LoadAll(context.TODO())
	if err != nil {
		logger.Errorf("err:%v", err)
		return err
	}

	resolveNodes := make(map[string]*discovery.Node)
	for _, svc := range svcs {
		leastNode := svc.Nodes[len(svc.Nodes)-1]
		resolveNodes[fmt.Sprintf("%s:%d", leastNode.Host, leastNode.Port)] = leastNode
	}

	eg := runtimeutil.NewErrGrp()
	for _, node := range resolveNodes {
		leastNode := node
		eg.Go(func() error {
			if err = resolve(leastNode.Host, leastNode.Port); err != nil {
				logger.Errorf("err:%v", err)
				return err
			}
			return nil
		})
	}
	if err = eg.Wait(); err != nil {
		logger.Errorf("err:%v", err)
	}

	return nil
}

func GetGrpcClientOptions() []grpc.DialOption {
	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	}

	if len(cfg.GrpcClientOptions) > 0 {
		opts = cfg.GrpcClientOptions
	}

	return opts
}

func resolve(nodeHost string, nodePort int) error {
	var (
		conn *grpc.ClientConn
		err  error
	)
	conn, err = grpc.NewClient(fmt.Sprintf("%s:%d", nodeHost, nodePort), GetGrpcClientOptions()...)
	if err != nil {
		logger.Errorf("err:%v", err)
		return err
	}

	cli := grpcreflect.NewClientAuto(context.Background(), conn)
	rpcSvcs, err := cli.ListServices()
	if err != nil {
		cli.Reset()
		return fmt.Errorf("failed to ListServices: %v", err)
	}

	for _, rpcSvc := range rpcSvcs {
		rpcSvcDesc, err := cli.ResolveService(rpcSvc)
		if err != nil {
			cli.Reset()
			// try only once here
			cli = grpcreflect.NewClientAuto(context.Background(), conn)
			rpcSvcDesc, err = cli.ResolveService(rpcSvc)
			if err != nil {
				cli.Reset()
				continue
			}
		}

		methods := rpcSvcDesc.GetMethods()
		svcName := rpcSvcDesc.GetName()
		for _, method := range methods {
			rpcMetadataMap.Store(fmt.Sprintf("%s.%s", rpcSvc, method.GetName()), &RpcMetadata{
				svcFullyQualifiedName: rpcSvc,
				svcName:               svcName,
				method:                method,
				invokeMethodName:      fmt.Sprintf("/%s/%s", rpcSvc, method.GetName()),
				reqPool: &sync.Pool{
					New: func() interface{} {
						return dynamicpb.NewMessage(method.GetInputType().UnwrapMessage())
					},
				},
				respPool: &sync.Pool{
					New: func() interface{} {
						return dynamicpb.NewMessage(method.GetOutputType().UnwrapMessage())
					},
				},
			})
		}
	}

	return nil
}

func ClearGrpcConns() {
	rpcConnMu.Lock()
	defer rpcConnMu.Unlock()
	for _, conn := range rpcConns {
		_ = conn.Close()
	}
	rpcConns = make(map[string]*grpc.ClientConn)
}

func WalkRpcMetadata(fn func(key string, meta *RpcMetadata) bool) {
	rpcMetadataMap.Range(func(key, value interface{}) bool {
		return fn(key.(string), value.(*RpcMetadata))
	})
}

func DeleteRpcMetadata(key string) {
	rpcMetadataMap.Delete(key)
}

func GetRpcMetadata(packageName, svcName, method string) (*RpcMetadata, bool) {
	rpcMeta, ok := rpcMetadataMap.Load(fmt.Sprintf("%s.%s.%s", packageName, svcName, method))
	if !ok {
		return nil, false
	}

	return rpcMeta.(*RpcMetadata), true
}
