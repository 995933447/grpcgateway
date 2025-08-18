# 用于实现代理http请求到grpc服务的网关框架
> **服务自动发现，增删grpc服务无需重启网关服务**
> **基于grpc反射服务，无需重启自动检测和更新grpc protobuf**

---
## ✨ 特性一览
- **高性能**：通过缓存grpc服务反射信息，grpc连接单例，以及构造请求和响应结构体缓冲池，实现快速路由和转发请求到grpc。
- **低开销**：缓存各grpc服务连接单例，复用grpc连接。
- **易用**：支持各类http请求格式转发到grpc服务，包括JSON,url query,x-www-form-urlencoded等，支持转发http request header和grpc response header。
- **服务发现**：基于发现自动发现新增和删除的grpc服务，无须重启。
- **热更新protobuf**: 基于grpc反射服务实现，无须重启自动检测和动态更新grpc protobuf。
- **组合式**：提供各种组合式api轻松实现各种业务系统合适的网关服务，如鉴权，流量过滤等。
