# home-nav

极简个人服务入口页，用来替代 Sun Panel 的导航入口能力。

## 目标

- 从配置文件读取服务分组和入口。
- 展示内网 / 外网访问链接。
- 展示服务标签和只读健康状态。
- 使用 Go 单二进制部署，减少运行时依赖。
- 使用 YAML 配置作为唯一真源。
- 可选单用户登录保护，不引入数据库或账号系统。

## 非目标

- 不做账号系统。
- 不做数据库。
- 不做 Docker 管理。
- 不接 Docker socket。
- 不提供真实服务的启动、停止、重启、删除等控制能力；页面里的删除只删除导航入口配置。
- 不做主题市场、插件系统、多租户或复杂后台。

## 本地运行

```bash
cp config.example.yaml services.yaml
go run ./cmd/home-nav -config services.yaml
```

默认监听：

```text
http://127.0.0.1:8080
```

健康检查：

```bash
curl http://127.0.0.1:8080/healthz
```

`/api/status` 在完成首次设置或关闭登录保护后可访问；启用登录时需要带登录 session。

## Docker

```bash
docker build -t home-nav:local .
cp config.example.yaml services.yaml
docker run --rm -p 8080:8080 -v "$PWD/services.yaml:/app/services.yaml" home-nav:local
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
- 如果配置里的 `auth.enabled: false`、`auth.username: admin`、`auth.password: change-me`、`auth.session_secret: change-this-to-at-least-32-random-characters` 仍然保持示例值，首次打开首页会进入 `/setup`。
- `/setup` 只允许从本机或局域网来源访问时完成首次设置；如果从公网访问未初始化实例，页面会拒绝设置并提示改用局域网地址或手动配置密码。
- 如果前面有 Nginx、Caddy 等反向代理，应传递真实客户端 IP（例如 `X-Forwarded-For` 或 `X-Real-IP`），否则服务端只能看到代理到 Go 进程的来源地址。
- 首次设置会写回 `services.yaml`，启用登录并生成随机 `auth.session_secret`。运行配置必须可写，Docker 挂载时不要使用只读挂载。
- `auth.enabled` 为 `true` 时，首页和 `/api/status` 需要登录，`/healthz` 仍保持公开。
- `auth.enabled` 为 `true` 时不能继续使用示例默认密码或示例 `session_secret`；真实 `auth.password` 和 `auth.session_secret` 只应放在私有运行配置里，不要提交到仓库。
- 登录 session 不设置自动过期时间；登录后会一直保持到用户手动退出、浏览器清理 Cookie，或服务端更换 `auth.session_secret`。
- 旧配置中的 `auth.session_ttl` 会被兼容读取，但不再参与登录有效期判断，新写回的配置不会继续输出该字段。
- 页面支持新增、编辑、删除入口并写回 YAML；如果用 Docker 挂载配置文件，`/app/services.yaml` 需要读写挂载。只读挂载可以浏览，但保存变更会失败。
- 页面里的删除只删除导航入口，不会删除、停止或重启真实服务。
- 页面支持编辑模式；编辑模式开启后可以拖拽图标调整排序，松手后会自动写回 YAML，右上角保存按钮可用于保存未完成的排序变更。未拖拽时左键点击图标进入编辑，关闭编辑模式后左键仍然直接跳转。
- 页面支持分组管理：可以新增分组、重命名分组、调整分组顺序并删除空分组；含有入口的分组不能直接删除，需要先移动或删除入口。
- 页面支持外网 / 内网访问模式切换；该偏好保存在浏览器本地，影响服务卡片左键默认打开的入口。
- `appearance.background_color`、`appearance.background_image` 和 `appearance.background_overlay` 控制整页背景。背景图可以使用 `/uploads/...` 路径或 `http(s)` 图片 URL；`background_overlay` 支持 `low`、`medium`、`high`，用于在不同明暗壁纸上保持图标和文字可读。
- 如果服务图标使用 `/uploads/...` 这类本地图标路径，需要配置 `assets.uploads_dir` 并把上传目录挂载到容器内；编辑页上传图标会写入 `icons/` 子目录。真实上传目录不要提交到仓库。
- 页面设置里的背景图上传复用 `assets.uploads_dir`，新上传壁纸会写入 `wallpapers/` 子目录，所以生产环境需要把 `/app/uploads` 持久化挂载并保持可写。
- 页面支持图库管理：顶部图库可以查看、筛选、上传、复制和删除 `assets.uploads_dir` 下的图片资源；入口编辑里的图库用于回填入口图标，页面设置里的图库用于设置页面背景。旧资源会按当前引用、扩展名和尺寸兼容识别，新上传资源按上传时选择的图标或壁纸类型归类。
- 图库删除只会删除 uploads 目录内的图片文件；如果资源正在被页面背景或入口图标引用，后端会拒绝删除，避免留下失效引用。
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
      - /path/to/uploads:/app/uploads
      - /path/to/icon-cache:/app/icon-cache
```
