# wadist k8s Deployment Baseline

## Capacity Model (internal/capacity.Compute)

`capacity.Compute(Inputs{...})` returns a `Plan` with:

```
accountsPerNode  = MaxLockConns           // one pinned advisory-lock conn per active account
connsPerNode     = MaxLockConns + MaxOpenConns + MaxOpenConns   // lock + biz + sqlDB pools
requiredMaxConnections = connsPerNode * nodes + HeadroomConns
redisMemoryMB    = ceil((accounts*(dailyKeyBytes+pacingKeyBytes) + queueDepth*taskBytes) * 2 / 1MiB)
```

With the defaults deployed here (`MAX_LOCK_CONNS=300`, `MaxOpenConns=50`, `HeadroomConns=50`, `replicas=2`):

| Parameter              | Value     |
|------------------------|-----------|
| accountsPerNode        | 300       |
| connsPerNode           | 400       |
| requiredMaxConnections | 850       |

## PG max_connections Derivation

`docker-compose.yml` sets `max_connections=700`. This covers:

- 2 nodes × 400 conns/node = 800 conns at capacity + 50 headroom
- 700 is the local dev baseline (single node); production multi-node sizing
  must use `capacity.Compute` and set `max_connections` accordingly.

## Redis --maxmemory Derivation

`docker-compose.yml` sets `--maxmemory 512mb`. Derived from the capacity formula:

```
accounts=300, dailyKeyBytes=64, pacingKeyBytes=64, queueDepth=1000, taskBytes=512
=> bytes = 300*(64+64) + 1000*512 = 38400 + 512000 = 550400
=> redisMemoryMB = ceil(550400 * 2 / 1MiB) = ceil(1100800/1048576) = 2 MB
```

The 512 MB baseline provides generous headroom for transient task payloads and
Redis internal fragmentation. `--maxmemory-policy noeviction` is mandatory:
asynq task loss = lost already-charged messages.

## Rolling Update / PDB / preStop / Readiness

### Zero-downtime rolling update

`maxUnavailable: 0` ensures at least `replicas` pods are Ready before the old
pod is terminated. `maxSurge: 1` allows one extra pod to start ahead, so the
transition is: start new → new passes /readyz → remove old.

### PodDisruptionBudget (pdb.yaml)

`minAvailable: 1` guarantees at least one replica survives voluntary
disruptions (node drain, cluster upgrades). With `replicas: 2` this prevents
both pods from being evicted simultaneously.

### preStop + terminationGracePeriodSeconds

`lifecycle.preStop: sleep 5` (WADIST_PRESTOP_DELAY=5s) gives the k8s
endpoint controller time to remove the pod from service endpoints before the
process receives SIGTERM. This prevents new requests routing to a draining pod.

After preStop, the process has `WADIST_SHUTDOWN_TIMEOUT=30s` (M8 Supervisor)
to drain active sessions and deregister from the cluster (M9 deregister hook).

`terminationGracePeriodSeconds: 45 >= 5 (preStop) + 30 (shutdown) + 10 (margin)`

### Readiness vs Liveness

- `/readyz` (periodSeconds 2, failureThreshold 1): gates traffic; the server
  marks itself not-ready at the start of shutdown so rolling updates stop
  routing new traffic immediately.
- `/healthz` (periodSeconds 10, failureThreshold 3): triggers pod restart only
  after 30s of hard failure, avoiding restart loops during transient slowness.
