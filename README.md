# home-nav

极简个人服务导航页，用来替代 Sun Panel 的导航入口能力。

## 目标

- 从配置文件读取服务分组和入口。
- 展示内网 / 外网访问链接。
- 支持搜索、标签筛选和只读健康状态。
- 使用 Go 单二进制部署，减少运行时依赖。
- 使用 YAML 配置作为唯一真源。
- 可选单用户登录保护，不引入数据库或账号系统。

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
curl http://127.0.0.1:8080/api/status
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

配置要点：

- `groups[].services[]` 是导航入口唯一真源。
- 每个服务至少配置 `id`、`name`、一个入口 URL 和 `health.type`。
- 健康检查支持 `disabled`、`http` 和 `tcp`。
- 后端按 `check_interval` 定时刷新状态缓存，页面和 `/api/status` 不会实时探测每个服务。
- 配置解析会拒绝未知字段、重复 ID、非法 URL 和缺失健康检查参数。
- `auth.enabled` 为 `true` 时，首页和 `/api/status` 需要登录，`/healthz` 仍保持公开。
- 真实 `auth.password` 和 `auth.session_secret` 只应放在私有运行配置里，不要提交到仓库。
- 页面支持编辑入口并写回 YAML；如果用 Docker 挂载配置文件，`/app/services.yaml` 需要读写挂载。只读挂载可以浏览，但保存编辑会失败。
- 如果服务图标使用 `/uploads/...` 这类本地图标路径，需要配置 `assets.uploads_dir` 并把图标目录挂载到容器内；真实上传图标目录不要提交到仓库。
- 如果服务图标使用 `mdi:nas` 这类在线图标名，服务端会通过 Iconify API 拉取 SVG。生产环境建议配置 `assets.icon_cache_dir` 并持久化挂载，避免每次重建后重新拉取。

真实生产部署建议：

```yaml
services:
  home-nav:
    image: home-nav:<tag>
    ports:
      - "127.0.0.1:18080:8080"
    volumes:
      - /path/to/services.yaml:/app/services.yaml
      - /path/to/uploads:/app/uploads:ro
      - /path/to/icon-cache:/app/icon-cache
```
