package main

import (
	"context"
	"errors"
	"fmt"
	"math/rand"

	"github.com/95933447/grpcgateway/example/client/echo"
	"github.com/995933447/microgosuit"
	"github.com/995933447/microgosuit/discovery"
	"github.com/995933447/microgosuit/grpcsuit"
	"google.golang.org/grpc"
)

func main() {
	err := microgosuit.InitSuitWithGrpc(context.TODO(), "../meta.json", "testschema", "test_discovery")
	if err != nil {
		panic(err)
	}

	err = microgosuit.ServeGrpc(context.TODO(), &microgosuit.ServeGrpcReq{
		RegDiscoverKeyPrefix: "test_discovery",
		SrvName:              "echo.Echo",
		IpVar:                "$inner_ip",
		Port:                 9111,
		RegisterCustomServiceServerFunc: func(server *grpc.Server) error {
			echo.RegisterEchoServer(server, &EchoService{})
			return nil
		},
		AfterRegDiscover: func(discovery discovery.Discovery, node *discovery.Node) error {
			fmt.Printf("======\nrun echo service success.\nhost:%s\nprot:%d\n======\n", node.Host, node.Port)
			return nil
		},
	})
	if err != nil {
		panic(err)
	}
}

type EchoService struct {
	echo.UnimplementedEchoServer
}

func (s *EchoService) BasicEcho(ctx context.Context, req *echo.EchoReq) (*echo.EchoResp, error) {
	var resp echo.EchoResp
	resp.Echo = req.Echo
	return &resp, nil
}

func (s *EchoService) InnerEcho(ctx context.Context, req *echo.EchoReq) (*echo.EchoResp, error) {
	return s.BasicEcho(ctx, req)
}

func (s *EchoService) NoAuthEcho(ctx context.Context, req *echo.EchoReq) (*echo.EchoResp, error) {
	return s.BasicEcho(ctx, req)
}

func (s *EchoService) NoAuthEchoErr(ctx context.Context, req *echo.EchoReq) (*echo.EchoResp, error) {
	r := rand.Intn(100)
	if r > 50 {
		return nil, errors.New("sys err")
	}
	return nil, grpcsuit.NewRpcErr(echo.ErrCode_ErrFail)
}
