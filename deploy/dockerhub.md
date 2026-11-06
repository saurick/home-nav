# Docker Hub 使用与发布

使用者只需要 Docker 和 Docker Compose，无需下载源码或安装 Go。运行配置、上传图片和图标缓存统一放在安装目录的 `data/` 下；更换镜像保留这个目录即可。镜像构建覆盖 `linux/amd64` 和 `linux/arm64`，实际可下载的版本以发布者的 Docker Hub 仓库为准。

使用者从“首次安装”开始；维护者从“发布配置”开始。本项目的目标镜像名为 `simon66255/home-nav`。

## 首次安装

以下命令在新建安装目录执行。已有实例不要重新复制示例配置，以免覆盖登录信息和导航入口。

1. 创建目录并拉取明确版本的镜像：

   ```sh
   mkdir -p home-nav/data/uploads home-nav/data/icon-cache
   cd home-nav
   export HOME_NAV_IMAGE=simon66255/home-nav:0.1.0
   docker pull "$HOME_NAV_IMAGE"
   ```

2. 将 [Docker Hub Compose](docker-compose.hub.yml) 保存为安装目录中的 `compose.yaml`。从镜像提取示例配置，不需要启动临时容器：

   ```sh
   init_container=$(docker create "$HOME_NAV_IMAGE")
   docker cp "$init_container:/app/services.yaml" ./data/services.yaml
   docker rm "$init_container"
   chmod 600 ./data/services.yaml
   printf 'HOME_NAV_IMAGE=%s\nHOME_NAV_USER=%s:%s\n' "$HOME_NAV_IMAGE" "$(id -u)" "$(id -g)" > .env
   ```

3. 编辑 `data/services.yaml`，将 `assets` 改成下面的容器内路径。按自己的环境修改 `access` 和示例服务地址；也可以保留至少一个空分组（`services: []`），登录后从页面添加入口。配置要求至少有一个分组。

   ```yaml
   assets:
     uploads_dir: /data/uploads
     uploads_url_prefix: /uploads/
     icon_cache_dir: /data/icon-cache
   ```

4. 启动并检查健康状态：

   ```sh
   docker compose up -d --wait
   docker compose ps
   curl -fsS http://127.0.0.1:8080/healthz
   ```

   健康端点应返回 `ok`。通过 `http://服务器局域网IP:8080` 打开首页并完成管理员设置，再验证新增入口、上传图片和刷新页面。首次设置只接受本机或局域网来源；公网部署须先通过局域网完成设置，或按 [配置说明](../README.md#配置) 手工配置认证。

Compose 读取 `.env`，可配置以下项目：

| 配置 | 默认值 | 用途 |
| --- | --- | --- |
| `HOME_NAV_IMAGE` | 必填 | 完整镜像名及版本，例如 `simon66255/home-nav:0.1.0` |
| `HOME_NAV_USER` | `0:0` | 容器运行用户；初始化命令写入当前账号的 UID:GID，保证写回文件仍属于安装账号 |
| `HOME_NAV_PORT` | `8080` | 主机访问端口 |
| `HOME_NAV_BIND` | `0.0.0.0` | 主机监听地址；使用同机反代时可设为 `127.0.0.1` |

`data/` 必须事先存在并可写，内含 `services.yaml`、`uploads/` 和 `icon-cache/`。Compose 挂载整个目录，以便配置文件原子替换；不要将该目录或配置挂载为只读。旧版的 [本地 Compose](docker-compose.yml) 保持原有文件挂载方式，不会自动迁移到这个目录。

## 升级与回退

1. 记录当前 `.env` 中的镜像版本与 `docker compose images`，备份 `compose.yaml`、`.env` 和 `data/`。配置含登录信息，备份应存放在私有目录。已有登录保护时，在升级前确认可正常登录。
2. 将 `.env` 中的 `HOME_NAV_IMAGE` 改为新版本，在安装目录执行：

   ```sh
   docker compose pull
   docker compose up -d --wait
   docker compose ps
   ```

3. 验证登录、导航入口、图片和保存操作。镜像健康检查只证明进程可以响应，不替代这些使用验证。
4. 需要回退时，将镜像改回原版本并重新启动，保留当前 `data/`。若版本说明涉及配置格式变化，先按该版本说明恢复相关字段；不要用旧完整配置覆盖升级后新增的入口或图片。

`latest` 跟随最近一次稳定版发布，日常使用建议固定版本号。不要在更新时删除 `data/`。

## 发布配置

1. 在 [Docker Hub](https://hub.docker.com/) 创建公开的 `home-nav` 仓库，记录实际命名空间。本项目发布到 `simon66255/home-nav`。
2. 为发布账号建立有目标仓库写入权限的 Docker Hub access token。在 GitHub 仓库 **Settings → Secrets and variables → Actions** 配置以下内容：

   | 类型 | 名称 | 值 |
   | --- | --- | --- |
   | Repository variable | `DOCKERHUB_USERNAME` | 用于登录的 Docker Hub 用户名 |
   | Repository variable | `DOCKERHUB_IMAGE` | 完整仓库名，例如 `simon66255/home-nav`，不带版本标签 |
   | Repository secret | `DOCKERHUB_TOKEN` | 有推送权限的 access token |

   组织仓库的镜像命名空间可以与登录用户名不同。token 只保存在 GitHub secret 或本机 Docker 凭据存储，不写入仓库、`.env` 或发布记录。

3. 将本轮要发布的代码和发布配置核对、测试并提交。按仓库约定确认提交和推送范围后，再推送版本标签；发布从标签对应的提交构建，未提交修改不会进入 CI 镜像。

## 自动发布

[Docker image 工作流](../.github/workflows/docker-publish.yml) 在推送 `v主版本.次版本.修订号` 标签时发布稳定版本。例如以下命令只在已确认发布范围并获准推送后执行：

```sh
git tag v0.1.0
git push origin v0.1.0
```

工作流先执行 Go 和页面脚本测试，再分别构建两种架构，使用 [容器冒烟检查](smoke-docker.py) 验证首次设置、配置写回、内嵌资源、图片上传及重建持久化。两种架构都通过后才登录并推送多架构镜像；登录前会拒绝缺失的发布配置或不符合格式的版本标签。

发布生成 `0.1.0`、`sha-<完整提交哈希>` 和 `latest` 标签，工作流摘要记录镜像 digest 和 Git commit，OCI labels 记录来源和版本。已有版本号不重复用于不同提交。推送完成后验证：

```sh
docker buildx imagetools inspect simon66255/home-nav:0.1.0
```

确认包含 `linux/amd64` 和 `linux/arm64`，并在一个独立安装目录按首次安装步骤拉取已发布版本做运行验证。没有数据库或迁移步骤。

Pull request 和 **Actions → Docker image → Run workflow** 仅测试、构建和冒烟，不登录 Docker Hub，也不推送镜像。手动验证应在版本标签发布前完成。实现流程参考 [Docker 官方多架构 CI](https://docs.docker.com/build/ci/github-actions/multi-platform/) 与 [标签和 labels](https://docs.docker.com/build/ci/github-actions/manage-tags-labels/)。

## 本地验证与手动发布

在仓库根目录执行。默认 Buildx 构建器支持多架构时可以直接使用；若不支持，单独创建构建器，不改变其他项目正在使用的默认构建器：

```sh
docker buildx create --name home-nav-release --driver docker-container --bootstrap
```

下面的命令指定该构建器；已有适用构建器时，将名字替换为实际名称。

```sh
go test ./...
node --test internal/server/web/*.test.cjs

docker buildx build --builder home-nav-release --platform linux/amd64 \
  --load -t home-nav:smoke-amd64 .
python3 -B deploy/smoke-docker.py --image home-nav:smoke-amd64 --platform linux/amd64

docker buildx build --builder home-nav-release --platform linux/arm64 \
  --load -t home-nav:smoke-arm64 .
python3 -B deploy/smoke-docker.py --image home-nav:smoke-arm64 --platform linux/arm64
```

冒烟使用独立的临时目录、Compose 项目和随机本机端口，测试后只清理本次创建的容器和临时数据。它不会读取私有 `services.yaml` 或调用示例服务；异构镜像运行需要 Docker 环境支持对应架构的模拟执行。

手动发布也应从已提交、工作区干净且通过上述验证的版本构建。登录后设置镜像名和版本，并在确认上传范围后执行：

```sh
docker login
export HOME_NAV_REPOSITORY=simon66255/home-nav
export HOME_NAV_VERSION=0.1.0
export HOME_NAV_REVISION=$(git rev-parse HEAD)

docker buildx build --builder home-nav-release \
  --platform linux/amd64,linux/arm64 \
  --label org.opencontainers.image.source=https://github.com/saurick/home-nav \
  --label org.opencontainers.image.revision="$HOME_NAV_REVISION" \
  --label org.opencontainers.image.version="$HOME_NAV_VERSION" \
  -t "$HOME_NAV_REPOSITORY:$HOME_NAV_VERSION" \
  -t "$HOME_NAV_REPOSITORY:sha-$HOME_NAV_REVISION" \
  -t "$HOME_NAV_REPOSITORY:latest" \
  --push .
```

`.dockerignore` 只允许构建所需的源码、依赖清单和示例配置进入上下文；私有 YAML、凭据、上传内容、部署工具和 `.git` 不会被发送给构建器。镜像中的默认配置只用于初始化，不替代使用者的持久化运行配置。
