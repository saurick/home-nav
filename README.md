# home-nav

极简个人服务入口页，用来替代 Sun Panel 的导航入口能力。

## 目标

- 从配置文件读取服务分组和入口。
- 展示内网 IP、内网域名和外网访问链接。
- 展示服务标签和只读健康状态。
- 使用 Go 单二进制部署，页面、样式、脚本和工具 SVG 随二进制内嵌，无需前端构建或运行时依赖。
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
- 入口可分别填写 `internal_url`（内网 IP）、`internal_domain_url`（内网域名）和 `external_url`（外网）。三个字段都接受完整的 HTTP 或 HTTPS URL，旧配置的 `internal_url` 保持有效。
- 页面顶部的“访问入口”下拉框可直接选外网、内网 IP 或内网域名的优先入口；选择会保存在当前浏览器。菜单显示每种入口已配置数量，这个数量不表示当前可以访问。缺少所选地址的服务会回退到其他已配置地址；卡片下方显示实际入口类型，回退时明确显示“外网回退”等提示。
- 没有独立 IP 页面时留空 `internal_url`；仅将 IP 请求重定向到域名的旧地址不算独立 IP 入口。
- 健康检查支持 `disabled`、`http` 和 `tcp`。
- 后端按 `check_interval` 定时刷新状态缓存，页面和 `/api/status` 不会实时探测每个服务。
- 点击卡片的状态点或入口标签，可查看实际访问地址、后台探测目标、结果和最近检查时间。正常表示该探测成功，探测地址可能与所选入口不同；它不代表其他入口的 HTTPS、页面或登录已验证。未监测服务隐藏状态点；状态接口更新失败时显示等待色并保留可查看的最近记录。
- 顶部搜索支持名称和标签，不区分大小写，多个词用空格分隔并同时匹配。搜索只筛选显示，不修改配置或排序；无结果时可清空恢复。按 `/` 或 `Ctrl/Cmd+K` 聚焦搜索，匹配文字会高亮；用上下方向键选择，`Enter` 打开当前结果，搜索框内按 `Esc` 清空。开启编辑模式会暂存搜索并显示完整列表，完成编辑后恢复原搜索。
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
- 保存入口、分组和背景后应用服务器返回的规范化数据，局部更新页面，保留搜索、编辑模式和页面位置。提交期间禁用表单，防止重复操作；字段错误会定位到对应输入框，连接失败保留填写内容。原生弹窗限制焦点、锁住背景滚动并支持 `Esc`，有未保存修改时可以继续编辑或放弃。
- 入口可用卡片星标或编辑表单固定到“常用入口”，`pinned: true` 会写回 YAML；取消置顶返回所属分组，排序和跨组移动仍保留置顶状态。分组折叠与舒适/紧凑显示密度仅保存在当前浏览器，搜索时自动展开匹配分组，快速跳转会展开目标分组。空分组和完全没有入口时仍可添加第一条。
- 页面内容随屏幕宽度扩展，工具栏与分组保持对齐；视口达到 2400 CSS 像素时，普通分组并排显示为两栏，常用入口跨栏。卡片间距随舒适、紧凑和手机布局调整；没有搜索结果提示或分组跳转时，列表工具栏自动收起。
- `GET /api/navigation` 返回当前导航数据供页面重新读取；权限与首页一致，仅包含分组、入口和外观，不含认证配置或服务器存储路径。改变探测目标会清除旧状态，修改前启动的探测不能回写到新目标或已删除入口。
- 页面支持全局编辑模式，图标和名称都能进入编辑；拖拽把手可在分组内和跨分组调整位置，松手后自动写回 YAML。只从把手启动拖拽，卡片其他区域仍允许手机滚动；`Esc`、系统取消触控或切走页面会取消本次拖动。聚焦把手后也能用方向键调整位置。连续调整按最新顺序保存；失败时保留当前位置并提供“重试保存”和“放弃排序”，放弃会重新读取服务器保存的顺序，避免操作结果未知时假定未保存。
- 页面支持分组管理：可以新增分组、重命名分组、调整分组顺序并删除空分组；含有入口的分组不能直接删除，需要先移动或删除入口。
- 优先入口影响服务卡片左键默认打开的地址。所选内网入口未填写时，优先使用另一种内网入口，再使用外网入口；外网模式则依次使用外网、内网 IP、内网域名。右键、卡片更多按钮或 `Shift+F10` 菜单可打开和复制已配置入口，缺失地址会合并说明；复制不受 HTTPS 限制，浏览器拒绝复制时提供手动复制窗口。
- `appearance.background_color`、`appearance.background_image` 和 `appearance.background_overlay` 控制整页背景。背景图可以使用 `/uploads/...` 路径或 `http(s)` 图片 URL；`background_overlay` 支持 `low`、`medium`、`high`，可调节壁纸明暗；导航卡片和控件使用稳定的深色底层保持文字对比度。颜色、图片、遮罩共同预览并在“保存设置”时一次写入；图库选图或上传只填入草稿，取消并放弃修改会恢复保存前的外观。
- 如果服务图标使用 `/uploads/...` 这类本地图标路径，需要配置 `assets.uploads_dir` 并把上传目录挂载到容器内；编辑页上传图标会写入 `icons/` 子目录。真实上传目录不要提交到仓库。
- 页面设置里的背景图上传复用 `assets.uploads_dir`，新上传壁纸会写入 `wallpapers/` 子目录，所以生产环境需要把 `/app/uploads` 持久化挂载并保持可写。
- 页面支持图库管理：顶部图库可以查看、筛选、上传、复制和删除 `assets.uploads_dir` 下的图片资源；入口编辑里的图库用于回填入口图标，页面设置里的图库用于回填背景草稿。点击图片直接选择；支持按名称搜索、分类筛选、按需加载和读取失败重试。上传文件会进入图库，入口或背景引用仍需保存对应表单后生效。旧资源会按当前引用、扩展名和尺寸兼容识别，新上传资源按上传时选择的图标或壁纸类型归类，并保留可搜索的文件名称前缀及防重名的随机后缀。
- 图库删除只会删除 uploads 目录内的图片文件；如果资源正在被页面背景或入口图标引用，后端会拒绝删除，避免留下失效引用。
- 如果服务图标使用 `mdi:nas` 这类在线图标名，服务端会通过 Iconify API 拉取 SVG。生产环境建议配置 `assets.icon_cache_dir` 并持久化挂载，避免每次重建后重新拉取。
- 常用服务的正式标识可从仓库内的 [品牌图标目录](deploy/official-icons/manifest.json) 安装到 uploads。图标保存为带内容摘要的本地路径，页面不再依赖外部图标请求；来源、校验、私有入口映射和恢复见 [图标部署](deploy/README.md#本地品牌图标)。

真实生产部署建议：

发布和可选的内网 HTTPS 证书同步说明见 [部署说明](deploy/README.md)。

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
