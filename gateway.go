package grpcgateway

import (
	"context"
	"fmt"
	"sync"

	"github.com/995933447/fastlog"
	"github.com/995933447/fastlog/logger"
	"github.com/995933447/fastlog/logger/writer"
	"github.com/995933447/microgosuit"
	"github.com/995933447/microgosuit/discovery"
	"github.com/995933447/microgosuit/factory"
	"github.com/995933447/runtimeutil"
	"github.com/jhump/protoreflect/desc"
	"github.com/jhump/protoreflect/grpcreflect"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/dynamicpb"
)

var rpcMetadataMap sync.Map // target.method => rpcMetadata

var (
	grpcConns  = make(map[string]*grpc.ClientConn)
	grpcConnMu sync.RWMutex
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
	LogConf
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
	Log          logger.LogConf
	LogAlertFunc writer.AlertFunc
}

var cfg *Conf

func Init(c *Conf) error {
	if err := checkConf(c); err != nil {
		return err
	}

	cfg = c

	if err := initLog(); err != nil {
		return err
	}

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
		fastlog.Errorf("grpc resolver init err: %v", err)
		return err
	}

	return nil
}

func InvokeGrpc(packageName, svcName, methodName string, paramsJson []byte, header map[string][]string, callOpts []grpc.CallOption) (*dynamicpb.Message, metadata.MD, error) {
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

	req, err := makeRpcReq(paramsJson, rpcMeta)
	if err != nil {
		return nil, nil, err
	}

	defer func() {
		req.Reset()
		rpcMeta.reqPool.Put(req)
	}()

	conn, err := makeConn(packageName, svcName)
	if err != nil {
		fastlog.Errorf("err:%v", err)
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

func makeConn(packageName, svcName string) (*grpc.ClientConn, error) {
	target := fmt.Sprintf("%s:///%s.%s", cfg.GrpcResolveSchema, packageName, svcName)
	grpcConnMu.RLock()
	conn, ok := grpcConns[target]
	grpcConnMu.RUnlock()
	if !ok {
		grpcConnMu.Lock()
		defer grpcConnMu.Unlock()
		conn, ok = grpcConns[target]
		if !ok {
			var err error
			conn, err = grpc.NewClient(fmt.Sprintf("%s:///%s.%s", cfg.GrpcResolveSchema, packageName, svcName), GetGrpcClientOptions()...)
			if err != nil {
				fastlog.Errorf("err:%v", err)
				return nil, err
			}

			grpcConns[target] = conn
		}
	}

	return conn, nil
}

func initLog() error {
	fastlog.SetModuleName(cfg.ServiceName)

	if err := fastlog.InitDefaultCfgLoader("", &cfg.Log); err != nil {
		return err
	}

	if err := fastlog.InitDefaultLogger(cfg.LogAlertFunc); err != nil {
		return err
	}

	return nil
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
				fastlog.Errorf("err:%v", err)
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
		fastlog.Errorf("err:%v", err)
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
				fastlog.Errorf("err:%v", err)
				return err
			}
			return nil
		})
	}
	if err = eg.Wait(); err != nil {
		fastlog.Errorf("err:%v", err)
		return err
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
		fastlog.Errorf("err:%v", err)
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
				return err
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

func WalkRpcMetadata(fn func(meta *RpcMetadata) bool) {
	rpcMetadataMap.Range(func(key, value interface{}) bool {
		return fn(value.(*RpcMetadata))
	})
}

func GetRpcMetadata(packageName, svcName, method string) (*RpcMetadata, bool) {
	rpcMeta, ok := rpcMetadataMap.Load(fmt.Sprintf("%s.%s.%s", packageName, svcName, method))
	if !ok {
		return nil, false
	}

	return rpcMeta.(*RpcMetadata), true
}
