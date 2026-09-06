# 什么是 Kite？

Kite 是面向平台团队的轻量、开源 Kubernetes 工作空间，将多集群运维、可观测性、访问控制和 AI 辅助排障整合到同一个界面中。

![Kite 仪表盘展示集群健康状态、工作负载、事件和资源用量](/screenshots/overview.png)

## 从这里开始

::: tip 最快体验路径
使用 Helm 安装 Kite，首次访问时通过端口转发打开界面，然后连接集群内的 Kubernetes API。整个初始设置不需要提前准备 kubeconfig。
:::

1. [安装 Kite](./installation) 并打开仪表盘。
2. 邀请团队前，先配置[用户](/zh/config/user-management)和 [RBAC](/zh/config/rbac-config)。
3. 需要集群指标时，再[连接 Prometheus](/zh/config/prometheus-setup)。

## 找到对应指南

- **快速找到任意资源：** [全局搜索](./global-search)
- **了解工作负载及其依赖关系：** [相关资源](./related-resources)
- **排查正在运行的 Pod：** [日志](./logs)与 [Web 终端](./web-terminal)
- **查看集群健康状态和用量：** [监控](./monitoring)
- **安装和管理应用：** [Helm 管理](./helm-management)
- **审查资源变更：** [资源历史](./resource-history)
- **使用 AI Agent 辅助排障：** [AI 助手](./ai-assistant)
- **连接私有或远程集群：** [Kite Cluster Agent](./kite-cluster-agent)

## 平台能力概览

### 观察

通过实时 CPU、内存和网络图表、Pod 日志、事件与资源关系，从异常现象快速定位所需上下文。

### 运维

创建和编辑 Kubernetes 资源、管理 Helm Release、打开 Pod 与 Node 终端、运行 kubectl，并通过 Kube Proxy 访问工作负载。

### 治理

使用 OAuth、MFA、Passkey、用户管理、RBAC、角色映射和审计日志，统一管理多集群中的团队访问。

## Kite 适合什么场景

Headlamp 和 Kubernetes Dashboard 都是优秀的单集群资源查看与操作工具。Kite 在此基础上增加了共享的多集群工作空间、团队治理、可观测性和 AI 辅助工作流。

如果你正在评估 Kite，请继续阅读[安装指南](./installation)。
