# vllmservice-operator

`vllmservice-operator` 是一个基于 Kubebuilder/controller-runtime 的 Kubernetes Operator，用来通过自定义资源 `VLLMService` 管理 vLLM OpenAI-compatible API 服务。

用户只需要提交一个 `VLLMService`，Operator 会根据声明式配置创建并持续同步：

- `Deployment`：运行 vLLM 容器。
- `Service`：暴露集群内访问入口。
- `HTTPRoute`：可选，将服务挂载到已有 Gateway API `Gateway`。
- `ServiceMonitor`：可选，让 Prometheus Operator 抓取 vLLM 指标。
- `status.conditions`：反馈 Deployment、PVC、Route、Monitoring 和整体可用状态。

## 项目结构

```text
api/v1alpha1/                         VLLMService API 类型定义
internal/controller/                  Reconcile 逻辑和单元测试
cmd/main.go                           controller manager 启动入口
config/crd/                           CRD manifests
config/rbac/                          controller 和用户辅助 RBAC
config/manager/                       manager Deployment
config/default/                       默认安装用 Kustomize 配置
config/prometheus/                    controller manager metrics 的 ServiceMonitor
config/network-policy/                metrics 入口 NetworkPolicy
config/samples/                       示例 CR，当前仍是 Kubebuilder 初始模板
test/e2e/                             Kind e2e 测试
```

## VLLMService API

- API Group：`aiinfra.example.com`
- Version：`v1alpha1`
- Kind：`VLLMService`
- Scope：Namespaced
- CRD：`vllmservices.aiinfra.example.com`

### 核心字段

| 字段 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `spec.image` | 是 | 无 | vLLM 镜像，例如 `vllm/vllm-openai:latest`。 |
| `spec.modelPath` | 是 | 无 | 容器内模型路径，会作为 `--model` 参数传给 vLLM。 |
| `spec.modelName` | 是 | 无 | 对外暴露的模型名，会作为 `--served-model-name`。 |
| `spec.resources` | 是 | 无 | vLLM 容器资源请求和限制，通常包含 GPU 资源。 |
| `spec.storage` | 是 | 无 | 模型存储 PVC 挂载配置。 |
| `spec.replicas` | 否 | `1` | Deployment 副本数。 |
| `spec.port` | 否 | `8000` | vLLM HTTP 服务端口。 |
| `spec.labels` | 否 | 空 | 追加到 Operator 管理资源上的 labels。 |
| `spec.runtimeClassName` | 否 | 空 | Pod `runtimeClassName`，可用于 GPU runtime。 |
| `spec.schedulerName` | 否 | `default-scheduler` | Pod 调度器名称。 |
| `spec.nodeSelector` | 否 | 空 | Pod 节点选择器。 |
| `spec.hostIPC` | 否 | `false` | 是否开启宿主机 IPC namespace。 |
| `spec.engineArgs` | 否 | 见下文 | vLLM engine 参数。 |
| `spec.gatewayRef` | 否 | 空 | 引用已有 Gateway，启用后创建 HTTPRoute。 |
| `spec.monitoring` | 否 | 空 | 启用后创建 ServiceMonitor。 |
| `spec.startupProbe` | 否 | 空 | 启用后创建 startupProbe。 |
| `spec.livenessProbe` | 否 | 空 | 启用后创建 livenessProbe。 |
| `spec.readinessProbe` | 否 | 空 | 启用后创建 readinessProbe。 |

### 存储配置

`spec.storage` 用来把已有 PVC 挂载到 vLLM 容器中：

| 字段 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `pvcName` | 是 | 无 | 当前命名空间下已有 PVC 名称。 |
| `mountPath` | 是 | 无 | PVC 在容器内的挂载路径。 |
| `readOnly` | 否 | `true` | 是否只读挂载。 |
| `subPath` | 否 | 空 | PVC 内部子路径。 |

Operator 还会检查 PVC 状态，并通过 `StorageReady` Condition 暴露是否已绑定。

### vLLM 启动参数

Operator 当前生成的容器参数如下：

```text
--model <spec.modelPath>
--served-model-name <spec.modelName>
--host 0.0.0.0
--port <spec.port 或 8000>
--dtype <spec.engineArgs.dtype 或 auto>
--max-model-len <spec.engineArgs.maxModelLen 或 4096>
--gpu-memory-utilization <spec.engineArgs.gpuMemoryUtilization 或 0.75>
--max-num-seqs <spec.engineArgs.maxNumSeqs 或 8>
```

`spec.engineArgs` 支持：

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `dtype` | `auto` | 可选值：`auto`、`half`、`float16`、`bfloat16`、`float`、`float32`。 |
| `maxModelLen` | `4096` | 最大上下文长度。 |
| `gpuMemoryUtilization` | `0.75` | vLLM 最多使用的 GPU 显存比例，范围 `0` 到 `1`。 |
| `maxNumSeqs` | `8` | 单次调度迭代最多处理的请求序列数。 |

### Gateway API 集成

`spec.gatewayRef` 表示引用一个已经存在的 Gateway。Operator 不创建 Gateway，只创建和维护同名 `HTTPRoute`。

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| `name` | 是 | 被引用的 Gateway 名称。 |
| `namespace` | 否 | Gateway 所在命名空间；为空时使用 `VLLMService` 所在命名空间。 |
| `sectionName` | 是 | Gateway listener 名称，会写入 `parentRefs.sectionName`。 |
| `host` | 是 | HTTPRoute 匹配的域名，会写入 `spec.hostnames`。 |

Operator 会校验 Gateway 是否存在、listener 是否存在，以及 listener 协议是否为 HTTP/HTTPS。没有配置 `gatewayRef` 时，会删除自己之前创建的 HTTPRoute，并将 `RouteReady` 标记为 `True`，原因是 `RouteNotRequired`。

### Prometheus 监控

`spec.monitoring.enabled=true` 时，Operator 会创建同名 `ServiceMonitor`：

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `enabled` | `false` | 是否启用 vLLM 服务指标抓取。 |
| `path` | `/metrics` | vLLM 暴露指标的 HTTP 路径。 |
| `interval` | `30s` | Prometheus 抓取间隔。 |
| `labels` | 空 | 追加到 `ServiceMonitor.metadata.labels`，常用于匹配 Prometheus 的 `serviceMonitorSelector`。 |

如果关闭 monitoring，Operator 会删除自己创建的同名 ServiceMonitor；如果发现同名但不是当前 `VLLMService` 控制的 ServiceMonitor，会跳过删除或覆盖。

### 健康探针

`startupProbe`、`livenessProbe` 和 `readinessProbe` 都只有在对应 `enabled=true` 时才会注入到容器。

默认值：

| 探针 | path | initialDelaySeconds | periodSeconds | timeoutSeconds | failureThreshold |
| --- | --- | --- | --- | --- | --- |
| `startupProbe` | `/health` | `30` | `10` | `5` | `60` |
| `livenessProbe` | `/health` | `30` | `30` | `5` | `3` |
| `readinessProbe` | `/health` | `30` | `30` | `5` | `3` |

## 使用示例

下面示例假设：

- 模型已经放在 PVC `qwen-model-pvc` 中。
- PVC 挂载到容器内 `/data/models`。
- 集群使用 `nvidia.com/gpu` 作为 GPU 资源名。
- 已存在 Gateway `ai-gateway`，listener 名称为 `http`。
- Prometheus Operator 会选择 label `release: prometheus` 的 ServiceMonitor。

```yaml
apiVersion: aiinfra.example.com/v1alpha1
kind: VLLMService
metadata:
  name: qwen25-15b
  namespace: default
spec:
  image: docker.m.daocloud.io/vllm/vllm-openai:latest
  modelPath: /data/models/Qwen2.5-1.5B-Instruct
  modelName: qwen2.5-1.5b-instruct
  replicas: 1
  port: 8000
  runtimeClassName: nvidia
  nodeSelector:
    accelerator: nvidia
  labels:
    app.kubernetes.io/part-of: ai-inference
  resources:
    limits:
      nvidia.com/gpu: "1"
      cpu: "4"
      memory: 16Gi
    requests:
      nvidia.com/gpu: "1"
      cpu: "2"
      memory: 8Gi
  storage:
    pvcName: qwen-model-pvc
    mountPath: /data/models
    readOnly: true
  engineArgs:
    dtype: auto
    maxModelLen: 4096
    gpuMemoryUtilization: "0.75"
    maxNumSeqs: 8
  startupProbe:
    enabled: true
    path: /health
    failureThreshold: 60
  livenessProbe:
    enabled: true
    path: /health
  readinessProbe:
    enabled: true
    path: /health
  gatewayRef:
    name: ai-gateway
    namespace: default
    sectionName: http
    host: qwen.example.com
  monitoring:
    enabled: true
    path: /metrics
    interval: 30s
    labels:
      release: prometheus
```

应用：

```sh
kubectl apply -f vllmservice.yaml
```

查看状态：

```sh
kubectl get vllmservice qwen25-15b -n default -o yaml
kubectl get deployment,svc,httproute,servicemonitor -n default
```

集群内调用：

```sh
kubectl run curl-vllm --rm -it --restart=Never --image=curlimages/curl -- \
  curl http://qwen25-15b.default.svc.cluster.local:8000/v1/models
```

## 状态字段

Operator 会更新 `status`：

| 字段 | 说明 |
| --- | --- |
| `observedGeneration` | 当前 status 对应的 `metadata.generation`。 |
| `phase` | 基于 Deployment 推导，可能是 `Pending`、`Running`、`failed`。 |
| `readyReplicas` | Deployment ready 副本数。 |
| `deploymentName` | Operator 创建或同步的 Deployment 名称。 |
| `serviceName` | Operator 创建或同步的 Service 名称。 |
| `httpRouteName` | Operator 创建或同步的 HTTPRoute 名称。 |
| `serviceMonitorName` | Operator 创建或同步的 ServiceMonitor 名称。 |
| `gatewayRefName` | 当前引用的 Gateway 名称。 |
| `gatewayRefNamespace` | 当前引用的 Gateway 命名空间。 |
| `message` | 当前阶段或异常的简要说明。 |
| `conditions` | 各组件就绪状态。 |

当前 Conditions：

| Condition | True 含义 |
| --- | --- |
| `DeploymentReady` | Deployment 已达到期望副本数并可用。 |
| `StorageReady` | `spec.storage.pvcName` 对应 PVC 已 Bound。 |
| `RouteReady` | 未启用 Gateway 路由，或 HTTPRoute 已被 Gateway 接受且引用解析成功。 |
| `MonitoringReady` | 未启用 monitoring，或 ServiceMonitor 已配置成功。 |
| `Available` | Deployment、Storage、Route、Monitoring 都处于就绪状态。 |

排查时优先看：

```sh
kubectl describe vllmservice <name> -n <namespace>
kubectl get vllmservice <name> -n <namespace> -o jsonpath='{.status.conditions}'
kubectl describe deployment <name> -n <namespace>
kubectl describe pvc <pvc-name> -n <namespace>
kubectl describe httproute <name> -n <namespace>
kubectl describe servicemonitor <name> -n <namespace>
```

## 安装前提

本项目当前依赖以下能力：

- Kubernetes 集群。
- `kubectl` 可访问目标集群。
- Docker 或兼容容器工具，用于构建 manager 镜像。
- Go `1.24.5` 或兼容版本，用于本地开发。
- Gateway API CRDs，因为 manager 注册并 watch `HTTPRoute`，并读取 `Gateway`。
- Prometheus Operator CRDs，因为 manager 注册并 watch `ServiceMonitor`。

如果目标集群暂时不需要 Gateway API 或 ServiceMonitor，需要先调整 controller 中对应的 scheme 注册、RBAC 和 `Owns(...)` watch 配置。

## 部署 Operator

构建并推送镜像：

```sh
make docker-build docker-push IMG=<registry>/vllmservice-operator:<tag>
```

安装 CRD：

```sh
make install
```

部署 controller manager：

```sh
make deploy IMG=<registry>/vllmservice-operator:<tag>
```

默认会安装到 `vllmservice-operator-system` 命名空间，并启用 controller manager 的 HTTPS metrics endpoint `:8443`。

检查：

```sh
kubectl get pods -n vllmservice-operator-system
kubectl get crd vllmservices.aiinfra.example.com
kubectl get clusterrole | grep vllmservice
```

## 卸载

先删除业务 CR：

```sh
kubectl delete vllmservice <name> -n <namespace>
```

卸载 controller manager：

```sh
make undeploy
```

卸载 CRD：

```sh
make uninstall
```

如果要忽略不存在的资源：

```sh
make undeploy ignore-not-found=true
make uninstall ignore-not-found=true
```

## 本地开发

常用命令：

```sh
make manifests      # 生成 CRD 和 RBAC
make generate       # 生成 deepcopy
make fmt            # go fmt
make vet            # go vet
make build          # 构建 bin/manager
make run            # 使用当前 kubeconfig 在本地运行 manager
make test           # envtest 单元测试
make lint           # golangci-lint
```

首次运行测试时，`make test` 会通过 Makefile 下载 `controller-gen`、`setup-envtest` 等工具到 `bin/`。

e2e 测试使用 Kind：

```sh
make test-e2e
```

默认 Kind 集群名为 `vllmservice-operator-test-e2e`。可以通过 `KIND_CLUSTER` 覆盖。

## 构建发布包

生成单文件安装包：

```sh
make build-installer IMG=<registry>/vllmservice-operator:<tag>
```

产物会写到：

```text
dist/install.yaml
```

用户可以通过下面方式安装：

```sh
kubectl apply -f dist/install.yaml
```

## 实现说明

Reconcile 主流程：

1. 读取 `VLLMService`。
2. 创建或更新同名 `Deployment`。
3. 创建或更新同名 `Service`。
4. 根据 `spec.monitoring.enabled` 创建、更新或删除同名 `ServiceMonitor`。
5. 根据 `spec.gatewayRef` 创建、更新或删除同名 `HTTPRoute`。
6. 检查 Deployment、PVC、HTTPRoute、ServiceMonitor 状态。
7. 更新 `status.conditions` 和摘要字段。

资源命名规则：

- Deployment：与 `VLLMService.metadata.name` 相同。
- Service：与 `VLLMService.metadata.name` 相同。
- HTTPRoute：与 `VLLMService.metadata.name` 相同。
- ServiceMonitor：与 `VLLMService.metadata.name` 相同。

核心 labels：

```text
app.kubernetes.io/name=vllmservice
app.kubernetes.io/instance=<VLLMService name>
app.kubernetes.io/managed-by=vllmservice-operator
```

Service 使用端口名 `http`，ServiceMonitor 和探针都通过该命名端口访问。

## 许可

Copyright 2026.

Licensed under the Apache License, Version 2.0.
