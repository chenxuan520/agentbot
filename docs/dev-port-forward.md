# 本地访问远端调试端口

记录怎么把跑在远端机器上的开发服务（例如 Vite preview 的 `4173`、调试中的 HTTP 端口）转发到本机浏览器。

适用机器：`root@2605:340:cd52:f00:3828:1def:ef85:7b43`（Hurricane Electric 公网 IPv6）。

## TL;DR

**直接 ssh 一条命令搞定，不要绕 jump。** `~/.ssh/config` 里已经配好别名 `target`。

```bash
ssh -f -N -L 14173:localhost:4173 target
```

浏览器打开 `http://localhost:14173` 即是远端 `4173`。

注意：

- 本机端口故意用 `14173` 而不是 `4173`，因为本机自己跑 dev server 时常占着 `4173`。可以自己换号。
- `localhost:4173` 这里的 `localhost` 是**远端视角**——远端 Vite 一般只 bind 在 `127.0.0.1:4173`，直连 IPv6 4173 会被拒，必须走 SSH 隧道。

停掉后台 tunnel：

```bash
pkill -f 'ssh -f -N.*14173:localhost:4173'
```

## ssh config

`~/.ssh/config` 末尾已有：

```
Host target
    HostName 2605:340:cd52:f00:3828:1def:ef85:7b43
    User root
    AddressFamily inet6
```

`AddressFamily inet6` 避免双栈解析在没有 IPv4 路径时卡顿。

认证走 Kerberos / GSSAPI 票据（公司 SSO），无密码无 key。票据 24h 过期，过期后 `kinit` 续。

## 为什么不走 jump

历史上习惯先 `ssh jump.byted.org` 再 `ssh root@target`，但 jump 上**任何端口转发都做不了**。验证过的失败方案：

| 方案 | 结果 | 原因 |
|---|---|---|
| `ssh -J jump root@target` | 失败 | jump 禁了 `direct-tcpip` 通道，报 `channel 0: open failed: unknown channel type` |
| `ssh -L 4173:localhost:4173 jump`（一条命令直达） | 失败 | 同上 |
| 登入 jump 后 `ssh -L ... root@target` | 失败 | jump 的 `ssh` 是 wrapper，不支持 `-L` / `-D` / `-R` 等转发参数，直接吐 usage |
| `ProxyCommand ssh jump nc %h %p` | 失败 | jump 也禁了远端 `exec`，报 `exec request failed on channel 0` |

结论：**`jump.byted.org` 是定制 SSH server，只允许交互式 SSH 跳转，禁所有 forwarding 能力**。

可行路线是绕开 jump 直连：

- 目标 IPv6 在 Hurricane Electric 公网段，全球可路由
- 本机办公网 / 家宽都有公网 IPv6 出口
- 目标机 sshd 监听 IPv6 且认 Kerberos 票据

## 排障 checklist

直连不通时按顺序查：

```bash
# 1. 本机有 IPv6 出口吗（应该返回一个 v6 地址）
curl -6 -sS https://api64.ipify.org

# 2. 目标 22 端口直连通吗
nc -6 -vz 2605:340:cd52:f00:3828:1def:ef85:7b43 22

# 3. Kerberos 票据没过期吧
klist

# 4. 本机端口是不是被自己占了
lsof -nP -iTCP -sTCP:LISTEN | grep -w 4173
```

一行总判：

```bash
nc -6 -vz -w 5 2605:340:cd52:f00:3828:1def:ef85:7b43 22 && klist -s && echo OK_TO_DIRECT || echo USE_JUMP
```

## 直连不通时的兜底（只能登录、不能转发）

换到没有 IPv6 出口的网（4G、酒店 wifi、部分客户现场）时，只能两段手动：

```bash
ssh jump
# 进入 jump shell 后
ssh root@2605:340:cd52:f00:3828:1def:ef85:7b43
```

**这种模式下没法做端口转发**（jump 限制如上）。要本地浏览远端 UI 只能改办法：

- 把笔记本切到手机 IPv6 热点
- 或者临时让远端 Vite 改 `--host` 监听公网 IPv6 + 防火墙放行 4173（注意安全风险）

## 一点合规提示

直连绕过了 jump 的审计日志。如果这台机器是公司资产 / 生产环境，需要按信安规定走 jump；个人实验机器无所谓。
