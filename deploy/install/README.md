# ongrid 部署手册

本目录随 `ongrid-<ver>.tar.xz` 发布包一同提供，供运维同学在目标主机（VPS / 裸金属服务器）上完成 ongrid 云端的安装、升级和卸载。

## 要求

- Linux amd64 / arm64（Ubuntu 22.04+ / Debian 12+ / CentOS Stream 9 / Rocky 9 均可）。
- Docker >= 24.0；Docker Compose v2（即 `docker compose` 子命令，不是旧版 `docker-compose`）。
- 至少 2 GB 内存、10 GB 可用磁盘。
- 以 root 身份或具备 sudo 权限的用户执行脚本。
- 可出公网访问 `docker.io`（如需拉取 MySQL、Prometheus 镜像；`prom/prometheus:v2.54.0` 由 docker compose 拉取，未随 tarball 发货）；`ongrid` 镜像已打包在发布包内，无需对外连接私有 registry。

## 两种安装形态

| | `--mode=compose`（默认） | `--mode=systemd` |
|---|---|---|
| **运行底座** | docker / docker-compose | 系统原生 systemd 单元 |
| **manager 进程** | `ongrid` 容器 | `ongrid.service`（`/usr/local/bin/ongrid`）|
| **依赖** | 同一份 docker-compose 起 mysql / prom / loki / tempo / qdrant / nginx / grafana | mariadb-server / nginx / grafana 走 apt-dnf 包管；prom / loki / tempo / qdrant 安装为 systemd 单元 |
| **何时选** | 默认推荐；开箱即用，少踩坑 | 禁 docker、合规要求、已有 systemd 运维栈 |
| **包大小** | ~430M（docker images 占大头） | 同上 + ~15M 裸 binary（`bin/ongrid` + `bin/ongrid-frontier`） |
| **卸载** | `uninstall.sh --purge` | `uninstall.sh --purge`（自动派发到 `systemd/uninstall-systemd.sh`）|

systemd 形态详细看 `systemd/README.md`。

## 安装

安装完成后，服务在目标主机 `<host>` 上的访问端点：

- Web UI：`https://<host>:8443/`
- Health check：`https://<host>:8443/healthz`
- HTTP redirect：`http://<host>:8800/`
- Edge tunnel：`<host>:40012`
- Prometheus：随 compose 启动，但默认不暴露到 host；从机器内或容器内验证。
- Grafana：随 compose 启动，通过 nginx 暴露为 `https://<host>:8443/grafana/`。

下面命令中的 `<arch>` 按目标服务器 CPU 选择 `amd64` 或 `arm64`。

```bash
# 1. 把发布包上传到目标主机
scp ongrid-v0.1.0-linux-<arch>.tar.xz user@vps:~/

# 2. 登录目标主机并解压
ssh user@vps
tar xf ongrid-v0.1.0-linux-<arch>.tar.xz
cd ongrid-v0.1.0-linux-<arch>

# 3. （可选）先编辑 .env.example，自定义端口或 OpenAI key
#    不编辑也行，install.sh 会把 .env.example 复制成 .env 并为空密码自动生成随机值。

# 4. 运行安装脚本
sudo ./install.sh
```

`install.sh` 的执行过程：

1. 自检 Docker 环境；非 root 自动 `sudo` 重入。
2. 创建 `/opt/ongrid/`（可通过 `ONGRID_INSTALL_DIR` 覆盖）。
3. 拷贝 `docker-compose.yml`、`nginx.conf`、`prometheus.yml`、`grafana/`、`edge/`、`VERSION` 到安装目录。
4. **生成自签 TLS 证书**（首次安装且 `certs/tls.crt` 不存在时）：通过临时 OpenSSL 配置生成 CN=ongrid、SAN 包含 `ongrid` / `localhost` / `127.0.0.1` 的 365 天证书，落到 `${INSTALL_DIR}/certs/`，私钥 `chmod 600`。脚本不交互，直接生成；末尾 banner 提示替换真证书。
5. `docker load -i images/ongrid.tar`、`images/frontier.tar`、`images/ongrid-web.tar` 加载所有镜像。
6. 若 `/opt/ongrid/.env` 不存在则从 `.env.example` 创建，并对空字段（`MYSQL_ROOT_PASSWORD`、`MYSQL_PASSWORD`、`ONGRID_JWT_SECRET`、`ONGRID_ADMIN_PASSWORD`）生成随机值，文件权限置 `600`。
7. `docker compose up -d` 启动 MySQL + ongrid + frontier + nginx + prometheus（ADR-009）。
8. 轮询 `https://localhost:${ONGRID_HTTP_PORT}/healthz`（nginx 透传到 manager，`-k` 跳过自签校验）最多 60 秒。
9. 打印安装摘要，包括 **Web URL**、**API URL** 与 **管理员初始密码**（只显示一次，务必立即记录）。

### 可选参数

| 选项 | 说明 |
|------|------|
| `--profile monitoring` | 兼容旧版本保留；ADR-009 之后 Prometheus 已升级为核心服务，此参数对 prometheus 启动不再有效（始终启动） |
| `--no-seed` | 跳过尾部 "bootstrap admin" 说明 |
| `--force` | 已有安装基础上重新安装；`.env` 被保留，不会清数据 |

## 升级

新版本发布包同样包含 `upgrade.sh`：

```bash
scp ongrid-v0.2.0-linux-<arch>.tar.xz user@vps:~/
ssh user@vps
tar xf ongrid-v0.2.0-linux-<arch>.tar.xz
cd ongrid-v0.2.0-linux-<arch>
sudo ./upgrade.sh
```

升级脚本会：

1. 先 `docker compose down`（保留命名卷，数据不丢）。
2. 覆盖 `docker-compose.yml`、`nginx.conf`、`prometheus.yml`、`grafana/`、`edge/`、`VERSION`。
3. **不触碰 `.env` 和 `certs/`**，运维之前的自定义配置 / 真证书全部保留。
4. `docker load` 新镜像（`ongrid` / `frontier` / `ongrid-web`），修改 `.env` 中的 `ONGRID_VERSION`。
5. `docker compose up -d` 启动新版。
6. 轮询 `https://localhost:${ONGRID_HTTP_PORT}/healthz`（`-k` 跳过自签校验）最多 90 秒（库迁移可能稍慢）。

数据库 schema 由 gorm AutoMigrate 在 ongrid 启动时自动处理。

## 卸载

```bash
# 只停止容器，保留数据卷和 /opt/ongrid
sudo ./uninstall.sh

# 彻底清除：删除 MySQL 数据卷、/opt/ongrid 和 ongrid 镜像
sudo ./uninstall.sh --purge --yes
```

`--purge` 是破坏性操作，没有 `--yes` 会交互式确认 `y/N`。

## 配置项说明

所有配置集中在 `/opt/ongrid/.env`（权限 `600`，仅 root 可读）：

| 变量 | 说明 | 空值处理 |
|------|------|----------|
| `ONGRID_VERSION` | 镜像 tag，升级时由脚本自动改写 | 不允许空 |
| `ONGRID_HTTP_PORT` | nginx HTTPS 对外端口（默认 443，ADR-008） | 默认 443 |
| `ONGRID_HTTP_REDIRECT_PORT` | nginx HTTP→HTTPS 跳转端口（默认 80），置 `0` 可禁用 | 默认 80 |
| `ONGRID_TUNNEL_PORT` | Edge 连接端口（默认 40012） | 默认 40012 |
| `ONGRID_METRICS_PORT` | Prometheus 抓取端口（默认 9100） | 默认 9100 |
| `PROM_PORT` | 留作历史兼容（ADR-009 后 Prometheus 默认不发布到 host） | 默认 9090（不发布） |
| `ONGRID_PROM_ENABLED` | manager 是否将 push_prom_samples 转发给 cloud Prometheus + 注册 query_promql AI 工具（ADR-009） | 默认 `true` |
| `ONGRID_PROM_URL` | manager 访问 cloud Prometheus 的 URL，默认走 docker 内网 | 默认 `http://prometheus:9090` |
| `ONGRID_PROM_REMOTE_WRITE_URL` | 精确 remote_write endpoint；用于 VictoriaMetrics / Mimir / Cortex 等兼容 TSDB。为空时使用 `ONGRID_PROM_URL + /api/v1/write` | 默认空 |
| `ONGRID_PROM_QUERY_URL` | `query_promql` 使用的 Prometheus-compatible 查询 API root；为空时使用 `ONGRID_PROM_URL` | 默认空 |
| `ONGRID_ALERT_ENABLED` | 是否启用内置主机阈值告警（基于 `push_host_metrics` fast path） | 默认 `true` |
| `ONGRID_ALERT_COOLDOWN` | 同一 edge + rule 的重复通知静默时间 | 默认 `10m` |
| `ONGRID_ALERT_CPU_PERCENT` / `_MEM_PERCENT` / `_DISK_USED_PERCENT` | CPU / 内存 / 磁盘使用率阈值，`0` 表示关闭该规则 | 默认 `90` |
| `ONGRID_ALERT_LOAD1` | 1 分钟 load average 绝对阈值，`0` 表示关闭该规则 | 默认 `0` |
| `ONGRID_NOTIFY_ENABLED` | 是否启用对外通知发送（告警 / 定时任务 / 主动 AIOps） | 默认 `false` |
| `ONGRID_NOTIFY_DEFAULT_CHANNELS` | 默认通知通道名，逗号分隔，如 `slack,feishu` | 默认 `log` |
| `ONGRID_NOTIFY_TIMEOUT` | 单通道发送超时 | 默认 `10s` |
| `ONGRID_NOTIFY_LOG_ENABLED` / `_NAME` | 本地日志 dry-run 通道 | 默认启用，名为 `log` |
| `ONGRID_NOTIFY_WEBHOOK_*` | 通用 JSON webhook 通道，支持 `URL` 和 `SECRET` HMAC 签名 | 默认关闭 |
| `ONGRID_NOTIFY_SLACK_*` | Slack incoming webhook 通道 | 默认关闭 |
| `ONGRID_NOTIFY_FEISHU_*` | 飞书 / Lark 自定义机器人 webhook 通道，支持 `SECRET` 签名 | 默认关闭 |
| `ONGRID_NOTIFY_DINGTALK_*` | 钉钉自定义机器人 webhook 通道，支持 `SECRET` 签名 | 默认关闭 |
| `MYSQL_ROOT_PASSWORD` | MySQL root 密码（容器内使用） | 空则自动生成 24 位随机 |
| `MYSQL_PASSWORD` | `ongrid` 应用库密码 | 空则自动生成 24 位随机 |
| `ONGRID_JWT_SECRET` | JWT 签名密钥 | 空则自动生成 64 位随机 |
| `ONGRID_JWT_ACCESS_TTL` / `_REFRESH_TTL` | Token 有效期 | 默认 `15m` / `720h` |
| `ONGRID_ADMIN_EMAIL` | 首次启动时自动建立的管理员邮箱 | 默认 `admin@ongrid.local` |
| `ONGRID_ADMIN_PASSWORD` | 首次启动时的管理员密码 | 空则自动生成 20 位随机，安装末尾打印一次 |
| `OPENAI_API_KEY` | OpenAI 密钥（留空则 AI Chat 接口返回 500） | 可为空 |
| `OPENAI_MODEL` / `OPENAI_BASE_URL` | OpenAI 模型与自定义 endpoint | 默认 `gpt-4o` |

自动生成策略：仅替换 `.env` 中以 `=` 为结尾的空值行，运维已填的值不会被覆盖。

## 数据存储

v0.7.45 起所有有状态服务（MySQL / Prometheus / Loki / Tempo / qdrant / Grafana）的数据卷**直接 bind-mount 到宿主机**，默认根路径 `/var/lib/ongrid`，可通过 `ONGRID_DATA_DIR` 覆盖：

```text
/var/lib/ongrid/
├── mysql/        # MySQL 数据 (uid 999)
├── prometheus/   # Prom TSDB (uid 65534)
├── loki/         # Loki chunks (uid 10001)
├── tempo/        # Tempo blocks (uid 10001)
├── qdrant/       # 向量 collection (root)
└── grafana/      # Grafana SQLite + plugins (uid 472)

/var/log/ongrid/  # manager slog 输出，可被宿主机 promtail / vector / fluent-bit 直接抓取
```

`install.sh` / `upgrade.sh` 会自动 `mkdir -p` + `chown` 到对应 uid。生产环境推荐：

- 把 `ONGRID_DATA_DIR` 指向独立 SSD / NVMe / NFS 挂载点；
- 关键卷（`mysql`、`prometheus`）单独走快速本地盘，`loki` / `tempo` 走容量盘。

### 数据备份

直接对宿主机目录做快照即可，不再需要 `docker run --rm -v <named-vol>:/data`：

```bash
# MySQL 热备（推荐）— 不停业务
sudo docker exec ongrid-mysql sh -c \
    'mysqldump -u root -p"$MYSQL_ROOT_PASSWORD" --single-transaction --routines ongrid' \
    | gzip > /backup/ongrid-mysql-$(date +%F).sql.gz

# Prom TSDB 快照（开启 admin API 后用 /api/v1/admin/tsdb/snapshot 更稳）
# 简单姿势：tar 宿主机目录
sudo tar czf /backup/prom-$(date +%F).tar.gz -C /var/lib/ongrid/prometheus .

# Loki / Tempo / qdrant 同理
sudo tar czf /backup/loki-$(date +%F).tar.gz   -C /var/lib/ongrid/loki .
sudo tar czf /backup/tempo-$(date +%F).tar.gz  -C /var/lib/ongrid/tempo .
sudo tar czf /backup/qdrant-$(date +%F).tar.gz -C /var/lib/ongrid/qdrant .
```

### 数据卷迁移（v0.7.45 前的安装升级到 v0.7.45+ 必读）

老版本（≤ v0.7.44）使用 docker named volumes（`ongrid_mysql_data` / `prometheus_data` / …）。新版本 compose 改成 bind-mount 后，**named volume 里的旧数据不会自动出现在 bind path**，直接 `docker compose up` 会启动到一个看似干净的空环境（设备列表空、告警历史空、Grafana dashboard 还在但 datasource 数据空）。

升级前选一条路：

**自动迁移（推荐，小数据量）**：

```bash
sudo ./upgrade.sh --migrate-volumes
```

`upgrade.sh` 会：

1. `docker compose down` 停掉旧栈；
2. 对每个 legacy named volume `docker run --rm alpine cp -a` 到对应 bind path；
3. `chown` 到正确 uid；
4. `docker compose up -d` 起新栈。

迁移结束后旧 named volume 仍然保留（不会被自动删），人工 review 完用 `docker volume rm ongrid_mysql_data prometheus_data loki_data tempo_data qdrant_data grafana_data ongrid_logs` 清理。

**手动迁移（大数据量、TSDB 几十 GB 建议）**：

```bash
sudo ./upgrade.sh --no-migrate-volumes      # 升级脚本会跳过自动 copy
# 然后人工 rsync：
sudo docker compose -f /opt/ongrid/docker-compose.yml down
for v in ongrid_mysql_data:mysql prometheus_data:prometheus loki_data:loki tempo_data:tempo qdrant_data:qdrant grafana_data:grafana ongrid_logs:ongrid; do
    SRC="${v%%:*}"
    DST="${v##*:}"
    if [[ "$DST" == "ongrid" ]]; then DSTDIR=/var/log/ongrid; else DSTDIR=/var/lib/ongrid/$DST; fi
    sudo docker run --rm -v "$SRC":/src:ro -v "$DSTDIR":/dst alpine sh -c 'cp -a /src/. /dst/'
done
sudo docker compose -f /opt/ongrid/docker-compose.yml up -d
```

## 生产环境推荐：外接观测栈

当客户已经有 **Prometheus / Loki / Tempo / Grafana / qdrant** 中的任意一套或全部，强烈建议把 ongrid 指过去，不要再起一份内部栈——少一份运维负担、统一查询面、共享告警。

| 子系统 | 切到外部的姿势 | 关停内部容器 |
|--------|----------------|--------------|
| **Prometheus** | `.env` 设 `ONGRID_PROM_URL=https://prom.example.com`，需要单独的 remote_write endpoint 再设 `ONGRID_PROM_REMOTE_WRITE_URL=https://prom.example.com/api/v1/write`；如需自签证书绕过设 `ONGRID_PROM_TLS_INSECURE=true` | `docker compose stop prometheus && docker compose rm -f prometheus` |
| **Loki** | `.env` 设 `ONGRID_LOG_URL=https://loki.example.com`；edge 侧 logs plugin 推送路径在 manager 颁发的 endpoint 上配置 | `docker compose stop loki` |
| **Tempo** | `.env` 设 `ONGRID_TRACE_QUERY_URL=https://tempo.example.com:3200`、`ONGRID_OTEL_ENDPOINT=tempo.example.com:4318`（manager → Tempo 的 OTLP HTTP） | `docker compose stop tempo` |
| **Grafana** | `.env` 设 `ONGRID_GRAFANA_INTERNAL_URL=https://grafana.example.com`；ongrid 调 Grafana API 自动创建 ongrid SA，需要管理员账号一次性 bootstrap：`GRAFANA_ADMIN_USER` / `GRAFANA_ADMIN_PASSWORD` | `docker compose stop grafana` |
| **qdrant** | `.env` 设 `ONGRID_QDRANT_URL=https://qdrant.example.com:6333`；如需 API key 再设 `ONGRID_QDRANT_API_KEY` | `docker compose stop qdrant` |

切完之后内部容器关掉，`/var/lib/ongrid/<service>` 的数据可以归档备份后删除。

外部栈版本最低兼容线：

- Prometheus ≥ 2.40（remote_write v2 + PromQL `query_range`）
- Loki ≥ 2.9（LogQL）
- Tempo ≥ 2.3（TraceQL + OTLP HTTP receiver）
- Grafana ≥ 10.4（service account API）
- qdrant ≥ 1.8（filter + payload search）

注意：**不要**同时启用内部+外部 Prometheus，会出现 remote_write 双写 + 查询面割裂。Loki / Tempo 同理。

## 访问

- **Web UI**：浏览器打开 `https://<host>/`（默认 443；自定义端口则 `https://<host>:${ONGRID_HTTP_PORT}/`）。首次访问由于自签证书，浏览器会提示"此连接不安全 / 证书无效"，点"高级 → 继续访问"即可，或参考下文替换真证书。
- **Grafana**：`https://<host>/grafana/`。
- **HTTP→HTTPS 跳转**：`http://<host>/` 由 nginx 自动 `301` 到 `https://<host>/`（除非将 `ONGRID_HTTP_REDIRECT_PORT` 设为 `0`）。
- **API**：所有 API 都在 `https://<host>/api/v1/...`，前端与 API 同源。
- **Edge tunnel**：默认 `<host>:40012`，edge 安装时 `ONGRID_CLOUD_ADDR` 指向该地址。

## 自签证书 / 真证书替换

`install.sh` 首跑时会自动在 `${INSTALL_DIR}/certs/` 生成自签 `tls.crt` + `tls.key`（CN=ongrid，SAN 包含 `localhost`、`127.0.0.1`），有效期 365 天。`upgrade.sh` 和 `--force install.sh` 都不会覆盖已有证书。

替换为正式证书（如 Let's Encrypt 拿到的 `fullchain.pem` + `privkey.pem`）：

```bash
sudo cp fullchain.pem /opt/ongrid/certs/tls.crt
sudo cp privkey.pem   /opt/ongrid/certs/tls.key
sudo chmod 600 /opt/ongrid/certs/tls.key
sudo docker compose -f /opt/ongrid/docker-compose.yml restart nginx
```

`nginx.conf` 也是 bind-mount 的，运维要改路由 / 调超时直接编辑 `/opt/ongrid/nginx.conf` 后 `restart nginx` 即可。

## Grafana

Grafana 现在和 Prometheus 一起随 compose 启动，但默认不直接暴露 host `3000` 端口；推荐统一从 nginx 同源入口访问：

- `https://<host>/grafana/`

部署约定：

- nginx 对 `/grafana/` 走和 `/prometheus/` 相同的 ongrid 会话校验；
- Grafana 以 anonymous viewer 模式运行，只接受 nginx 反代后的内部访问；
- 默认自动 provision 一个 Prometheus datasource：
  - name: `ongrid-prometheus`
  - uid: `ongrid-prometheus`
  - url: `http://prometheus:9090/prometheus`
- 默认自动 provision 一个 `服务器详情` dashboard：
  - uid: `ongrid-server-detail`
  - 变量：`edge_id`
  - 面板：CPU、内存、Load 1m、网络接收速率

验证：

```bash
ssh root@<host> 'docker exec ongrid-grafana wget -qO- http://localhost:3000/api/health'
curl -k -I https://<host>:8443/grafana/
```

## 登录验证

安装脚本末尾打印的 `password` 是管理员初始密码，**只显示一次**（也保存在 `/opt/ongrid/.env`）。首次登录（注意 `-k` 跳过自签证书校验，换成真证书后可以去掉）：

```bash
curl -sk -X POST https://<host>/api/v1/auth/login \
     -H 'Content-Type: application/json' \
     -d '{"email":"admin@ongrid.local","password":"<上面生成的密码>"}' \
     | jq -r '.data.access_token'
```

拿到的 JWT 用于后续 API 调用：

```bash
TOKEN=<上一步输出>
curl -sk https://<host>/api/v1/orgs -H "Authorization: Bearer $TOKEN" | jq
```

## Edge 安装

云端跑起来后，通过 `CreateEdge` API 注册边缘节点，拿到 `access_key` 与 `secret_key`，然后在目标受管主机上：

```bash
# 1. 从云端主机拷贝 edge 目录过去
scp -r /opt/ongrid/edge user@target:~/ongrid-edge

# 2. 在 target 上安装
ssh user@target
cd ~/ongrid-edge
sudo ONGRID_CLOUD_ADDR=ongrid.example.com:40012 \
     EDGE_ACCESS_KEY=<ak> \
     EDGE_SECRET_KEY=<sk> \
     ./install-edge.sh
```

脚本会：

- 根据 `uname -s/-m` 挑选 `ongrid-edge-<os>-<arch>` 二进制，拷贝到 `/usr/local/bin/ongrid-edge`。
- 创建系统用户 `ongrid-edge`、日志目录 `/var/log/ongrid-edge`。
- 渲染 `/etc/ongrid-edge/ongrid-edge.env` 并安装 systemd unit。
- `systemctl enable --now ongrid-edge` 并打印状态与最近 20 行日志。

卸载：`sudo /path/to/install-edge.sh --uninstall`。

> 注：edge 二进制当前通过环境变量读取配置（`ONGRID_EDGE_CLOUD_ADDR` / `_ACCESS_KEY` / `_SECRET_KEY`），因此发布包中使用 `ongrid-edge.env.example` 模板（而非 yaml），systemd unit 通过 `EnvironmentFile=` 注入。

## Troubleshooting

- **部署状态确认**（在目标主机上）：
  ```bash
  ssh root@<host> 'cd /opt/ongrid && docker compose --env-file .env ps'
  ssh root@<host> 'curl -kfsS https://localhost:8443/healthz'
  ssh root@<host> 'docker exec ongrid-prometheus wget -qO- http://localhost:9090/-/ready'
  ```
- **端口被占用**（`docker compose up` 报 `bind: address already in use`）：编辑 `.env` 里的 `ONGRID_HTTP_PORT`（默认 443）/ `ONGRID_HTTP_REDIRECT_PORT`（默认 80）/ `ONGRID_TUNNEL_PORT` / `ONGRID_METRICS_PORT`，重跑 `docker compose -f /opt/ongrid/docker-compose.yml --env-file /opt/ongrid/.env up -d`。把 `ONGRID_HTTP_REDIRECT_PORT=0` 可以彻底关掉 80→443 跳转监听。
- **`invalid IP address in add-host: "host-gateway"`**：目标机 Docker daemon 太旧，不支持 Docker 20.10 引入的 `host-gateway` 特殊值。新版 `install.sh` / `upgrade.sh` 会自动把 `.env` 里的 `ONGRID_HOST_GATEWAY` 写成 Docker bridge 网关 IP；已经遇到该错误的机器可直接重跑 `sudo ./install.sh`。如仍失败，手动执行 `ip -4 addr show docker0` 查网关（通常是 `172.17.0.1`），写入 `/opt/ongrid/.env`：`ONGRID_HOST_GATEWAY=<网关IP>` 后再 `cd /opt/ongrid && sudo docker compose --env-file .env up -d`。
- **MySQL healthcheck 超时**：首次拉起冷启动慢，observe `docker logs ongrid-mysql`。安装脚本最多等 60 秒，ongrid 容器会一直等到 mysql healthy；如 60s 内没进入 healthy，手动看 MySQL 日志。
- **`/healthz` 不通**：`docker logs ongrid -n 200`，常见是 `.env` 里 `ONGRID_JWT_SECRET` 为空或 `ONGRID_DB_DSN` 错误。
- **AI Chat 接口 500**：`OPENAI_API_KEY` 没填。要么填 key，要么不调用 chat 接口。
- **Edge 连不上**：检查云端 `ONGRID_TUNNEL_PORT`（默认 40012）防火墙；edge 侧 `journalctl -u ongrid-edge -n 100` 看 `dial tcp ... i/o timeout` 是否路径不通。

## Prometheus + AI PromQL（ADR-009）

ADR-009 之后 Prometheus 升级为**核心服务**。默认随 `install.sh` / `upgrade.sh` 一起拉起，**不发布**到 host（仅 docker 内网 `http://prometheus:9090`）。它承担两件事：

1. **被动接收 manager remote_write**：edge 通过 `push_prom_samples` 把开集 series 推到 manager，manager 自动加 `edge_id` + `ongrid_source` label 后转发到 prometheus 的 remote_write 接收口（CLI flag `--web.enable-remote-write-receiver` 已开）。
2. **服务 AI agent 的 `query_promql` 工具**：aiops tool registry 在 `cfg.Prom.Enabled=true` 时注册该工具，让 LLM 通过 `/api/v1/query_range` 跑任意 PromQL（30s 超时硬约束）。

数据保留：默认 90 天 / 20GB cap，由 docker-compose 中的 `--storage.tsdb.retention.time=90d` 和 `--storage.tsdb.retention.size=20GB` flag 控制。要调整，编辑 `/opt/ongrid/docker-compose.yml` 后 `docker compose up -d` 重起 prometheus 服务。

**关闭** AI PromQL 链路（保留容器但 manager 不转发）：

```bash
# 编辑 .env 把 ONGRID_PROM_ENABLED=false，然后：
sudo docker compose -f /opt/ongrid/docker-compose.yml --env-file /opt/ongrid/.env restart ongrid
# 容器仍跑；如果连容器都不想要：
sudo docker compose -f /opt/ongrid/docker-compose.yml --env-file /opt/ongrid/.env stop prometheus
```

**直接查 PromQL**（无需 host 端口，从云端 box 内访问）：

```bash
sudo docker exec ongrid-prometheus wget -qO- \
    'http://localhost:9090/api/v1/query?query=up' | jq
```

**备份**：`prometheus_data` 是命名卷，备份姿势同 `ongrid_mysql_data`：

```bash
sudo docker run --rm \
    -v prometheus_data:/data \
    -v "$(pwd)":/out \
    alpine sh -c 'tar czf /out/prometheus-$(date +%F).tar.gz -C /data .'
```

`/opt/ongrid/prometheus.yml` 是 bind-mounted scrape config，改完跑 `docker compose restart prometheus` 或 `curl -XPOST http://prometheus:9090/-/reload`（`--web.enable-lifecycle` 已开）。

**备份**已迁移到宿主机目录（v0.7.45+），见上文「数据存储 → 数据备份」小节，不再需要 `docker run --rm -v prometheus_data:/data`。
