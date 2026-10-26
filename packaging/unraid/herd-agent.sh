#!/bin/bash
# 牧云 Agent(Herd)启动脚本 —— Unraid / 任意 Linux 通用
# 放置路径:/boot/config/plugins/user.scripts/scripts/herd-agent/script.sh
# 开机自启:User Scripts 插件 → 本脚本 Schedule 设为 "At Startup of Array"
#
# 变更历史(相对旧版脚本):
#   1. HERD_SERVER 由 ws://10.20.40.14:8080(本机面板)→ wss://moonshepherd.aowu.tech(公网面板,TLS)
#   2. 二进制升级为 herd-linux-amd64(品牌定名 MoonShepherd 后的新版;旧名 shepherd-linux-amd64)
#   3. 启动前 pkill 旧实例,幂等,重复执行不会双跑(同机双 Agent 会注册成两个节点)
#   4. nohup 后台运行 + 日志落盘,适配 User Scripts 开机自启

WORKDIR=/mnt/user/herd-workdir
BINARY=herd-linux-amd64            # 未上传新二进制时先临时改回 shepherd-linux-amd64

# 停旧实例(兼容新旧二进制名)
pkill -f "$BINARY" 2>/dev/null
pkill -f shepherd-linux-amd64 2>/dev/null
sleep 1

# 面板凭据失效重新注册时自动备份一次(注册成功后自动生成新凭据;
# 需要再次重注册,手动删除 credentials.json.old 后重跑本脚本即可)
[ -f $WORKDIR/credentials.json ] && [ ! -f $WORKDIR/credentials.json.old ] && mv $WORKDIR/credentials.json $WORKDIR/credentials.json.old

# 首连注册:在下方赋值块中加一行 HERD_TOKEN=rt_xxx(面板「节点注册」生成;
# 已注册的节点不加,Token 为一次性,注册成功即作废)
HERD_SERVER=wss://panel.example.com \
HERD_CRED_FILE=$WORKDIR/credentials.json \
nohup $WORKDIR/$BINARY >> $WORKDIR/herd.log 2>&1 &

echo "herd Agent 已后台启动 (PID $!)"
echo "日志:tail -f $WORKDIR/herd.log"

# 首连注册(仅凭据失效/全新面板时):在面板「节点注册」生成一次性 Token,
# 在上方环境变量区加一行 HERD_TOKEN=rt_xxx 再执行本脚本。
