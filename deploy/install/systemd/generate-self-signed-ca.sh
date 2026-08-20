#!/usr/bin/env bash
# generate-self-signed-ca.sh — 在 manager 主机上生成自签 root CA + broker server cert
#
# 用途：边端通过 ONGRID_EDGE_TLS_CA_FILE pin root CA，
#   broker 使用此脚本生成的 server cert 终止 TLS。
#
# 输入：DOMAIN（DDNS 域名，例 broker.example.com）
# 输出（manager 主机本地 /etc/ongrid/tls/）：
#   ca.pem     — root CA（0644，分发给边端）
#   broker.crt — server cert（0644）
#   broker.key — server key（0640 root:ongrid，仅 frontier 可读）
#   ca.key     — root CA 私钥（0600 root:root，运行期零消费，勿分发）
#
# pitfall prevention：
#   - SAN 含域名（边端通过域名连 broker）
#   - -sha256 签名算法
#   - 证书有效但不配 InsecureSkipVerify（边端 buildDialer 用 RootCAs 验证）
#
# 使用：sudo bash generate-self-signed-ca.sh broker.example.com
set -euo pipefail

if [[ $# -lt 1 ]]; then
    echo "用法: $0 <DDNS-域名>" >&2
    echo "例:   $0 broker.example.com" >&2
    exit 1
fi

DOMAIN="$1"
TLS_DIR="/etc/ongrid/tls"

# 前置检查
if [[ $EUID -ne 0 ]]; then
    echo "错误：需要 root 权限（写 /etc/ongrid/tls/）" >&2
    exit 1
fi

command -v openssl >/dev/null 2>&1 || {
    echo "错误：openssl 未安装" >&2
    exit 1
}

mkdir -p "$TLS_DIR"
cd "$TLS_DIR"

# 幂等保护：已存在 root CA 时拒绝重跑（重新生成会使所有 pin 旧 CA 的边端静默失联）
if [[ -f ca.pem ]]; then
    echo "错误：$TLS_DIR/ca.pem 已存在。重新生成会使已分发边端失联。" >&2
    echo "如确要重置，先手动 mv 备份整个目录并重新分发 ca.pem。" >&2
    exit 1
fi
if [[ -z "${LAN_IP:-}" ]]; then
    echo "提示：未设置 LAN_IP（可选第二参数），证书 SAN 仅含 ${DOMAIN}/localhost/127.0.0.1" >&2
fi

echo "==> 生成 root CA（ca.key + ca.pem）"
openssl genrsa -out ca.key 4096
openssl req -x509 -new -key ca.key -sha256 -days 3650 \
    -out ca.pem \
    -subj "/CN=Ongrid Broker CA"

echo "==> 生成 broker server cert（broker.key + broker.csr + broker.crt）"
openssl genrsa -out broker.key 2048
openssl req -new -key broker.key \
    -out broker.csr \
    -subj "/CN=${DOMAIN}"

# SAN 扩展文件（证书 SAN 含域名）
cat > san.ext <<EOF
[req]
distinguished_name = req_distinguished_name
req_extensions = v3_req
prompt = no

[req_distinguished_name]
CN = ${DOMAIN}

[v3_req]
keyUsage = keyEncipherment, dataEncipherment, digitalSignature
extendedKeyUsage = serverAuth
subjectAltName = @alt_names

[alt_names]
DNS.1 = ${DOMAIN}
DNS.2 = localhost
IP.1 = 127.0.0.1
IP.2 = ${LAN_IP:-127.0.0.1}
EOF

openssl x509 -req -in broker.csr \
    -CA ca.pem -CAkey ca.key -CAcreateserial \
    -out broker.crt \
    -days 825 -sha256 \
    -extfile san.ext -extensions v3_req

echo "==> 设置权限"
chmod 0644 ca.pem broker.crt
chmod 0600 ca.key
chmod 0640 broker.key
chown root:root ca.key 2>/dev/null || true
chown root:ongrid broker.key 2>/dev/null || true

# 清理中间文件
rm -f broker.csr ca.srl san.ext

echo ""
echo "==> 完成。产物："
ls -la "$TLS_DIR"/{ca.pem,broker.crt,broker.key}
echo ""
echo "下一步："
echo "  1. 将 ca.pem SCP 到边端（路径 /etc/ongrid-edge/ca.pem）"
echo "  2. 边端配置 ONGRID_EDGE_TLS_CA_FILE=/etc/ongrid-edge/ca.pem"
echo "  3. frontier.yaml TLS 段指向 $TLS_DIR/broker.crt + broker.key"
echo "  4. manager 配置 ONGRID_FRONTIER_TLS_CA_FILE=$TLS_DIR/ca.pem"
