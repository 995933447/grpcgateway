package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/995933447/grpcgateway/example/client/echo"
	"google.golang.org/grpc"
)

func TestBenchEcho(t *testing.T) {
	conn, err := grpc.NewClient("192.168.2.225:9111", grpc.WithInsecure())
	if err != nil {
		panic(err)
	}

	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < 10000; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err = echo.NewEchoClient(conn).BasicEcho(context.TODO(), &echo.EchoReq{
				Echo: "hello world",
			})
			if err != nil {
				panic(err)
			}
		}()
	}
	wg.Wait()

	// 10000并发 time elapsed: 1.188055666s 所以grpc的单个流的最大并发限制默认不是如网上所说的100
	t.Logf("time elapsed: %s\n", time.Since(start))
}
