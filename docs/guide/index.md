# What is Kite?

Kite is a lightweight, open-source Kubernetes workspace for platform teams. It brings multi-cluster operations, observability, access control, and AI-assisted troubleshooting into one interface.

![Kite dashboard showing cluster health, workloads, events, and resource usage](/screenshots/overview.png)

## Start here

::: tip Fastest evaluation path
Install Kite with Helm, use port forwarding for the first visit, then connect the in-cluster Kubernetes API. You can complete the initial setup without preparing a kubeconfig.
:::

1. [Install Kite](./installation) and open the dashboard.
2. [Set up users](/config/user-management) and [RBAC](/config/rbac-config) before inviting your team.
3. [Connect Prometheus](/config/prometheus-setup) when you are ready to add cluster metrics.

## Find the right guide

- **Find any resource quickly:** [Global Search](./global-search)
- **Understand a workload and its dependencies:** [Related Resources](./related-resources)
- **Investigate a running Pod:** [Logs](./logs) and [Web Terminal](./web-terminal)
- **Track cluster health and usage:** [Monitoring](./monitoring)
- **Install and manage releases:** [Helm Management](./helm-management)
- **Review resource changes:** [Resource History](./resource-history)
- **Troubleshoot with an AI agent:** [AI Assistant](./ai-assistant)
- **Connect private or remote clusters:** [Kite Cluster Agent](./kite-cluster-agent)

## Platform at a glance

### Observe

Use real-time CPU, memory, and network charts, live Pod logs, events, and resource relationships to move from a symptom to its context.

### Operate

Create and edit Kubernetes resources, manage Helm releases, open Pod and Node terminals, run kubectl, and access workloads through Kube Proxy.

### Govern

Manage team access with OAuth, MFA, passkeys, users, RBAC, role mappings, and audit logs across multiple clusters.

## Where Kite fits

Headlamp and Kubernetes Dashboard are strong tools for inspecting and operating individual clusters. Kite adds a shared workspace for multi-cluster operations, built-in team governance, observability, and AI-assisted workflows.

If you are evaluating Kite, continue with the [installation guide](./installation).
