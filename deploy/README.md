# 部署与内网证书同步

Home Nav 镜像在开发机或 CI 构建，目标服务器只加载镜像和启动容器。私有 `services.yaml`、uploads 与图标缓存通过挂载持久化，更新镜像时保留这些目录。镜像标签绑定 Git commit，部署前保留原 compose 与镜像作为回滚点。

## 内网 HTTPS 证书

`lan-cert-sync.py` 是独立的主机维护工具，不由导航页面运行。它同步上游已经签发的证书；证书签发和 DNS 凭据仍由上游续期任务管理。依赖 Python 3、OpenSSL 1.1.1 或更新版本、OpenSSH，以及目标网关原有的检查与热加载命令。

| 步骤 | 校验与结果 |
| --- | --- |
| 拉取 | 专用 SSH key、固定已核对的 host key，仅读取两个 PEM 文件 |
| 验证 | 完整 CA 链、至少 14 天有效期、全部探测域名、证书与私钥匹配；拒绝更早到期的证书 |
| 部署 | 先保存原文件及权限，再原子替换各文件；检查配置并热加载 |
| 生效 | 使用 SNI 和受信 CA 对所有配置域名做真实 TLS 握手，并核对证书指纹 |
| 失败 | 写入失败结果并退出非零；文件已变更时恢复原文件与权限，再检查并热加载 |

未发生文件变更时仍会检查实际握手，不会仅根据磁盘文件判断成功。`--dry-run` 只拉取、验证和列出文件差异，不修改目标文件，也不证明新证书已在网关生效。工具不重启应用、修改 DNS 或调整网络访问规则。

### 配置与安装

1. 在目标主机建立专用 SSH key。上游 `authorized_keys` 使用受限命令，允许该 key 只读固定证书目录：

   ```text
   restrict,command="/bin/tar -C /path/to/source-cert -cf - fullchain.pem privkey.pem" ssh-ed25519 <public-key> lan-cert-sync
   ```

   `known_hosts_file` 必须来自已确认的上游 host key，不能关闭 host key 检查。撤销上游这条专用 key 即可停止该读取能力。

2. 根据 [配置示例](lan-cert-sync.example.json) 写入私有 `/etc/home-nav/lan-cert-sync.json`，目录 0700、文件 0600。实际域名、IP、证书路径与密钥路径不提交到仓库。`targets` 定义要同步的文件、属主、权限和网关命令；`probes` 列出需要验证的全部 HTTPS 域名与连接地址。目标必须是已存在的文件，Docker 应挂载整个证书目录，单文件挂载不适用原子替换。

3. 将脚本安装至 `/usr/local/sbin/home-nav-lan-cert-sync.py`，安装 `.service` 与 `.timer` 至 `/etc/systemd/system/`。先执行验证，再启动任务：

   ```sh
   python3 /usr/local/sbin/home-nav-lan-cert-sync.py --config /etc/home-nav/lan-cert-sync.json --dry-run
   systemctl daemon-reload
   systemctl start home-nav-lan-cert-sync.service
   systemctl enable --now home-nav-lan-cert-sync.timer
   ```

   timer 每日北京时间 04:10 后五分钟内执行，适用于上游 03:23 的续期任务；其他环境按上游时间调整。`Persistent=true` 会补执行错过的检查。

### 运行核对与恢复

```sh
systemctl list-timers home-nav-lan-cert-sync.timer
systemctl status home-nav-lan-cert-sync.service
journalctl -u home-nav-lan-cert-sync.service -n 30 --no-pager
cat /var/lib/home-nav-lan-cert-sync/last-result.json
```

失败不会标记为已同步。`last-result.json` 保存检查时间、证书指纹、期限、变更目标与回滚目录；错误日志不输出 PEM 或私钥。状态目录及回滚副本只允许 root 读取。每次证书变更都会保存一份原文件；清理时保留当前证书对应的回滚副本。

自动恢复失败时先停止 timer，根据错误里的回滚目录及其 `manifest.json` 恢复 cert/key 文件、原属主和权限，再运行检查与热加载命令并核对实际 TLS。不要删除仍被运行容器挂载的证书目录。

独立签发、使用不同密钥要求或需要硬件导入的证书应单独维护，不自动加入泛域名同步任务。

## 本地验证

```sh
go test ./...
python3 -B -m unittest discover -s deploy -p 'test_*.py'
```

Python 测试使用临时 CA 和临时文件，验证公私钥不匹配、域名不覆盖、短期限、不可信链、非法归档、旧证书保护、失败恢复与未变更证书的运行核对，不访问真实证书或生产网关。
