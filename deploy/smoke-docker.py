#!/usr/bin/env python3
"""在临时 Compose 项目验证镜像、配置写回和重建后的持久化，不访问真实服务。"""

import argparse
import base64
import http.cookiejar
import json
import os
from pathlib import Path
import secrets
import shutil
import subprocess
import tempfile
import urllib.parse
import urllib.request


def docker(*args, env=None):
    result = subprocess.run(
        ["docker", *args], env=env, text=True, capture_output=True, check=False
    )
    if result.returncode:
        raise RuntimeError(result.stderr.strip() or result.stdout.strip())
    return result.stdout.strip()


def check(condition, message):
    if not condition:
        raise RuntimeError(message)


def smoke(image, platform):
    project = "home-nav-smoke-" + secrets.token_hex(5)
    with tempfile.TemporaryDirectory(prefix=project + "-") as directory:
        root = Path(directory)
        data = root / "data"
        (data / "uploads").mkdir(parents=True)
        (data / "icon-cache").mkdir()
        compose_path = root / "compose.yaml"
        shutil.copyfile(Path(__file__).with_name("docker-compose.hub.yml"), compose_path)
        env = dict(
            os.environ,
            HOME_NAV_IMAGE=image,
            HOME_NAV_BIND="127.0.0.1",
            HOME_NAV_PORT="0",
            HOME_NAV_USER=f"{os.getuid()}:{os.getgid()}",
            DOCKER_DEFAULT_PLATFORM=platform,
        )

        def compose(*args):
            return docker(
                "compose", "--project-name", project, "--file", str(compose_path),
                *args, env=env,
            )

        init_container = None
        try:
            init_container = docker("create", "--platform", platform, image)
            config_path = data / "services.yaml"
            docker("cp", init_container + ":/app/services.yaml", str(config_path))
            docker("rm", init_container)
            init_container = None

            config = config_path.read_text()
            check('uploads_dir: ""' in config, "镜像示例缺少上传目录配置")
            check('icon_cache_dir: ""' in config, "镜像示例缺少图标缓存配置")
            check("\ngroups:" in config, "镜像示例缺少导航分组")
            config = config.replace('uploads_dir: ""', "uploads_dir: /data/uploads")
            config = config.replace('icon_cache_dir: ""', "icon_cache_dir: /data/icon-cache")
            # 探测使用空导航，避免依赖示例中的内网地址和外部服务。
            config = config.split("\ngroups:", 1)[0] + (
                "\ngroups:\n  - id: smoke\n    name: Smoke\n    services: []\n"
            )
            config_path.write_text(config)
            config_path.chmod(0o600)

            compose("up", "--detach", "--wait", "--wait-timeout", "90")
            container_id = compose("ps", "--quiet", "home-nav")
            image_id = docker("inspect", "--format", "{{.Image}}", container_id)
            actual_platform = docker(
                "image", "inspect", "--format", "{{.Os}}/{{.Architecture}}", image_id
            )
            check(actual_platform == platform, "运行镜像的架构不匹配: " + actual_platform)
            check(
                docker("inspect", "--format", "{{.State.Health.Status}}", container_id) == "healthy",
                "镜像健康检查未通过",
            )

            opener = urllib.request.build_opener(
                urllib.request.ProxyHandler({}),
                urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()),
            )

            def origin():
                endpoint = compose("port", "home-nav", "8080")
                return "http://127.0.0.1:" + endpoint.rsplit(":", 1)[1]

            base_url = origin()

            def request(path, body=None, method=None, content_type=None):
                headers = {"Content-Type": content_type} if content_type else {}
                req = urllib.request.Request(
                    base_url + path, data=body, method=method, headers=headers
                )
                with opener.open(req, timeout=10) as response:
                    return response.geturl(), response.read()

            check(request("/healthz")[1] == b"ok\n", "健康端点内容不匹配")
            check(request("/")[0].endswith("/setup"), "未初始化的镜像没有进入首次设置")
            password = secrets.token_urlsafe(24)
            setup = urllib.parse.urlencode({
                "username": "smoke-admin", "password": password,
                "confirm_password": password,
            }).encode()
            request("/setup", setup, content_type="application/x-www-form-urlencoded")
            check("enabled: true" in config_path.read_text(), "首次设置没有写回配置")

            request(
                "/api/groups", json.dumps({"name": "Container smoke"}).encode(),
                content_type="application/json",
            )
            check("Container smoke" in config_path.read_text(), "导航编辑没有写回配置")
            for asset in ("app.js", "app.css", "theme.js", "theme.css"):
                check(len(request("/assets/" + asset)[1]) > 0, "缺少内嵌资源: " + asset)

            png = base64.b64decode(
                "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jRusAAAAASUVORK5CYII="
            )
            boundary = "home-nav-" + secrets.token_hex(10)
            upload = (
                f"--{boundary}\r\nContent-Disposition: form-data; name=\"asset_type\"\r\n\r\nwallpaper\r\n"
                f"--{boundary}\r\nContent-Disposition: form-data; name=\"file\"; filename=\"smoke.png\"\r\n"
                "Content-Type: image/png\r\n\r\n"
            ).encode() + png + f"\r\n--{boundary}--\r\n".encode()
            uploaded = json.loads(request(
                "/api/uploads", upload, content_type="multipart/form-data; boundary=" + boundary
            )[1])["url"]
            check(request(uploaded)[1] == png, "上传图片无法读回")
            request(
                "/api/settings",
                json.dumps({
                    "background_color": "#123abc", "background_image": uploaded,
                    "background_overlay": "medium",
                }).encode(),
                method="PUT", content_type="application/json",
            )
            saved = config_path.read_bytes()
            cache_marker = data / "icon-cache" / "smoke-marker"
            cache_marker.write_text("persistent")

            compose("up", "--detach", "--force-recreate", "--wait", "--wait-timeout", "90")
            base_url = origin()
            navigation = json.loads(request("/api/navigation")[1])
            check(any(g["name"] == "Container smoke" for g in navigation["groups"]), "重建丢失导航配置或登录会话")
            check(navigation["appearance"]["background_image"] == uploaded, "重建丢失背景配置")
            check(request(uploaded)[1] == png, "重建丢失上传图片")
            check(config_path.read_bytes() == saved, "重建覆盖了运行配置")
            check(cache_marker.read_text() == "persistent", "重建丢失图标缓存目录")
            print(f"PASS {platform}: 健康检查、首次设置、配置保存、内嵌资源、上传和重建持久化")
        except Exception:
            print(compose("logs", "--no-color", "--tail", "30"))
            raise
        finally:
            compose("down", "--remove-orphans")
            if init_container:
                docker("rm", "--force", init_container)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", required=True)
    parser.add_argument("--platform", required=True, choices=["linux/amd64", "linux/arm64"])
    args = parser.parse_args()
    smoke(args.image, args.platform)


if __name__ == "__main__":
    main()
