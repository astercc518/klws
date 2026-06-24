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

Shutdown sequence (all delays are pre-drain):

1. **k8s preStop sleep 5 s** — k8s runs the preStop hook *before* sending
   SIGTERM. This 5 s window lets the endpoint controller propagate the removal
   from Service endpoints so no new traffic is routed to this pod.
2. **SIGTERM → app flips /readyz → 503** — on SIGTERM the app calls
   `SetReady(false)`. The readiness probe (`failureThreshold: 1`,
   `periodSeconds: 2`) detects this within ~2 s and k8s removes the pod from
   endpoints (belt-and-suspenders with step 1). The app then sleeps
   `PreStopDelay` (WADIST_PRESTOP_DELAY=5 s) to allow that propagation.
3. **Supervisor.Shutdown drains** — in-flight asynq tasks are drained within
   `WADIST_SHUTDOWN_TIMEOUT=30 s`. The M9 deregister hook fires here: any
   accounts owned by this node are released and the takeover scanner on a
   surviving/new pod re-acquires them via advisory-lock fencing, ensuring
   account-session continuity.

Total pre-drain delay ≈ preStop(5 s) + PreStopDelay(5 s) = 10 s, well within
`terminationGracePeriodSeconds: 45 >= 5 (preStop) + 5 (PreStopDelay) + 30 (shutdown) + 5 (margin)`.

**Zero-downtime for this system** means *account-session continuity* via M9
takeover, not HTTP request draining. Asynq work is queue-pulled (not
HTTP-routed), so `/readyz` gates the metrics scrape endpoint and signals
rolling-update readiness, while in-flight sends are drained by
`Supervisor.Shutdown`.

### Readiness vs Liveness

- `/readyz` (periodSeconds 2, failureThreshold 1): gates traffic and scrape
  availability; flipped to 503 at the start of shutdown so k8s stops routing
  and rolling updates see the pod as not-ready within ~2 s.
- `/healthz` (periodSeconds 10, failureThreshold 3): triggers pod restart only
  after 30 s of hard failure, avoiding restart loops during transient slowness.
