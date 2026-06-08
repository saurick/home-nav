# home-nav

极简个人服务导航页，用来替代 Sun Panel 的导航入口能力。

## 目标

- 从配置文件读取服务分组和入口。
- 展示内网 / 外网访问链接。
- 支持搜索、标签筛选和只读健康状态。
- 使用 Go 单二进制部署，减少运行时依赖。

## 非目标

- 不做账号系统。
- 不做数据库。
- 不做 Docker 管理。
- 不接 Docker socket。
- 不提供服务启动、停止、重启、删除等控制能力。
- 不做主题市场、插件系统、多租户或复杂后台。

## 本地运行

```bash
go run ./cmd/home-nav -config config.example.yaml
```

默认监听：

```text
http://127.0.0.1:8080
```

健康检查：

```bash
curl http://127.0.0.1:8080/healthz
```

## Docker

```bash
docker build -t home-nav:local .
docker run --rm -p 8080:8080 -v "$PWD/config.example.yaml:/app/services.yaml:ro" home-nav:local
```

或使用 compose：

```bash
docker compose -f deploy/docker-compose.yml up -d
```

## 配置

真实运行时建议使用 `services.yaml`，不要提交包含真实内网地址、外网域名、数据目录或密钥的私有配置。

示例见 [config.example.yaml](config.example.yaml)。

