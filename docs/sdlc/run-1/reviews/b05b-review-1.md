# Review: B-05b (D34) — commit ca192d92

Branch `worktree-agent-a19946e28b11e7103`. Reviewed the full diff (32 files), the consumer code of receiving, workflow-ts,
integration-hub-ts, pricing, scheduling and prediction-py, the platform Kafka client, and the dev and stage Argo CD wiring.

## Verdict: CHANGES REQUIRED

The KafkaUsers, ACLs, Role and SecretStore are correct and narrow. The two renames are complete. The pricing chart fix and
the stage handling are right. However, the dev enablement does not deliver a running workflow-ts trigger (findings 1 and 2),
and the new CI check misses a defect that would crash 13 of the 20 consumers still off as soon as they are turned on
(finding 3). Findings 1 to 3 are Major. The rest are Minor or Nit.

## Findings

### 1. Major: turning on the workflow-ts trigger in dev makes the whole Temporal worker crash-loop until all three source topics exist
- **Where:**
  - `deploy/envs/dev/values/workflow.yaml:6-8`
  - `services/workflow-ts/src/worker.ts:33`
  - `services/workflow-ts/src/eventConsumer.ts:176`
- **Failure scenario:** on a fresh dev cluster, `depot.tms.load`, `depot.tms.tender` and `depot.budget.plan` do not exist yet.
  - `auto.create.topics.enable: false` is set in `deploy/kafka/kafka.yaml:60`.
  - No KafkaTopic declares these topics. Debezium creates each one only when the first outbox row of that aggregate is relayed.
  - kafkajs `consumer.subscribe()` refreshes metadata for the topic. It retries `UNKNOWN_TOPIC_OR_PARTITION` and then throws.
  - That throw happens inside the top-level `await startEventConsumer(...)` in `worker.ts`, which has no catch, so the process exits.
  - The pod keeps restarting until transport has released a load and sent a tender reply, and budgeting has submitted a plan.
  - While it restarts, every workflow in dev is down, not just the tender trigger: approvals, payment runs, ECO, CAPA, party merge and agent runs.
  - Receiving uses franz-go, which keeps retrying metadata for a missing topic and does not hit this.
  - Confidence: high on the worker path. The kafkajs behaviour is from library knowledge and was not reproduced, because there is no cluster.
- **Fix, either of:**
  - Declare KafkaTopics in `deploy/kafka` for the source topics of every consumer dev turns on: the three above, plus receiving's `depot.receiving.receipt`, `depot.receiving.ersselfbill` and `depot.privacy.erasure`. Debezium skips creating a topic that already exists. Then extend `test_dev_consumers_that_are_on_can_start` to require a synced KafkaTopic for every topic of a consumer that is on.
  - Or make `startEventConsumer` non-fatal: log, back off and retry in the background, as pricing's cache consumer does.

### 2. Major (pre-existing, blocks this enablement): the workflow pod cannot start under `runAsNonRoot` with a non-numeric image user
- **Where:**
  - `services/workflow-ts/Dockerfile:10` (`USER node`)
  - `services/workflow-ts/charts/workflow/templates/deployment.yaml:19-25` (`runAsNonRoot: true`, no `runAsUser`)
- **Failure scenario:** the image's user is the name `node`, and the pod sets no numeric `runAsUser`.
  - The kubelet refuses the container with `container has runAsNonRoot and image has non-numeric user (node), cannot verify user is non-root`, and the pod sits in `CreateContainerConfigError`.
  - So the consumer this commit turns on never runs.
  - This commit did not introduce the problem. But the commit and the CI check claim this consumer is "able to start", and it is not.
  - integration-hub avoids it with `runAsUser: 1000` (its chart's `deployment.yaml:21`).
- **Fix:**
  - Add `runAsUser: 1000` and `runAsGroup: 1000` to the workflow pod securityContext, or change the Dockerfile to `USER 1000:1000`.
  - Optionally, have the dev-start test assert a numeric `runAsUser` when the image's `USER` is not numeric.

### 3. Major: 13 consumer charts mount the credentials at 0440 without an fsGroup, and the CI check only looks at the two dev consumers
- **Where:**
  - `deploy/scripts/test_kafka_consumers.py:1115`: the fsGroup assertion sits only in the dev-on render test.
  - The charts, each a `templates/consumer-deployment.yaml` unless noted:
    - audit
    - billing (`podSecurityContext` in values has no fsGroup)
    - fulfillment (no pod securityContext at all)
    - inventory (no pod securityContext at all)
    - production
    - production `shopfloor-consumer-deployment.yaml`
    - purchasing (no pod securityContext at all)
    - receivables (`podSecurityContext` in values has no fsGroup)
    - revrec (`podSecurityContext` in values has no fsGroup)
    - scheduling (`podSecurityContext` in values has no fsGroup)
    - search
    - shopfloor (no pod securityContext at all)
    - tax
- **Failure scenario:**
  - The Secret volume files are `root:root 0440`.
  - These images run as the distroless nonroot user, 65532. I checked the `FROM`/`USER` lines of audit, search, tax, production, purchasing, inventory and scheduling.
  - So the moment a consumer is turned on, `kafka.ConfigFromEnv` fails with `read KAFKA_PASSWORD_FILE: permission denied` and the pod crash-loops.
  - `DEV_OFF` gives "no dev database Secret" as the only blocker, which is incomplete.
  - The charts that do set `fsGroup: 65532` are fine: edi, finreport, intercompany, payables, prediction, receiving, workflow and pricing (now). integration-hub sets fsGroup 1000 and runs as uid 1000, which is also fine.
  - The CI check passes today because none of the 13 is on in dev.
- **Fix:**
  - Add `fsGroup: 65532` to those 13 pod specs.
  - Move the fsGroup assertion into `test_chart_templates_wire_the_kafka_user_credentials` for every block, either by rendering each chart with the block's brokers and secret set, or with a template regex. Then a chart cannot carry a KafkaUser while being unable to read its password.

### 4. Minor: the dev workflow trigger starts workflows that cannot run any activity, and the budget plan URL still names a `depot` namespace
- **Where:**
  - `deploy/envs/dev/values/workflow.yaml:11`
  - `services/workflow-ts/src/activityRegistry.ts:14-33`
  - `services/workflow-ts/charts/workflow/values.yaml:24`
- **Failure scenario:**
  - Dev sets no `serviceIdentity.signingKeySecret`. So `serviceTokenSignerFromEnv` throws, `buildActivities` registers no activities, and the worker logs `no activities are registered`.
  - Every `tenderWorkflow` or `budgetPlanWorkflow` the new trigger starts then retries activity tasks that no worker serves.
  - The `transportUrl` fix has no effect until a signing key exists.
  - The trigger also consumes `depot.budget.plan`, but `budgetingUrl` stays at `http://budgeting.depot.svc:8080`, the same wrong namespace the commit fixed for transport.
- **Fix:**
  - Set `budgetingUrl: http://budgeting.hello-ledger.svc:8080` in the dev and stage workflow values (budgeting's ingress policy already lists `workflow` as a peer).
  - Either give dev a workflow signing key, or record in the D34 as-built note that the trigger starts workflows that stall until one exists.

### 5. Minor: the CI check accepts a Role that can read every Secret in `kafka`
- **Where:** `deploy/scripts/test_kafka_consumers.py:1081-1082`
- **Failure scenario:** the check collects `resourceNames` from the `get` rules and asserts every rule is `secrets`/`get`. It does not require each rule to have `resourceNames`.
  - Verified by mutation in a scratch copy: I appended a second rule, `{apiGroups: [""], resources: [secrets], verbs: [get]}`, to `consumer-credentials-reader.yaml`.
  - `test_kafka_consumers.py` and `test_argocd_apps.py` both still passed (56 tests OK).
  - With that rule, the kafka-users SecretStore can read every Secret in `kafka`, including `depot-cluster-ca` (the CA private key), every other KafkaUser's password and debezium's credentials.
  - ClusterRoleBindings and bindings in other namespaces that name the ServiceAccount are not checked either.
- **Fix:**
  - Assert that every rule of a Role bound to the store's ServiceAccount has non-empty `resourceNames`.
  - Assert that no ClusterRoleBinding names that ServiceAccount.
- **Note:** the committed Role is correct: `get` only, on exactly `depot-cluster-ca-cert`, `receiving-consumer` and `workflow-ts-consumer`, with no list or watch.

### 6. Minor: some consumers slip past the code derivation (two verified by mutation)
- **Where:**
  - `deploy/scripts/test_kafka_consumers.py:479-500` (Go)
  - `deploy/scripts/test_kafka_consumers.py:596-623` (TypeScript)
- **Scenarios, all run in a scratch copy, not the worktree:**
  - **(a) Verified: an extra kafkajs subscribe slips.** The TS topics come from the `return { group, topics }` config object, not from `consumer.subscribe()`. Adding `await consumer.subscribe({ topic: "depot.sales.order" })` to `eventConsumer.ts` left all 7 tests passing. At runtime the consumer would get `TOPIC_AUTHORIZATION_FAILED`.
  - **(b) Verified: a Go group override slips.** Go uses only the default-group argument of `ConfigFromEnv`. Changing receiving's `cfg.Group = erasureConsumerGroup` to `cfg.Group = "receiving-erasure-v2"` left all tests passing. At runtime the group join fails with `GROUP_AUTHORIZATION_FAILED`. A `cfg.Topics = append(...)` after the call would slip the same way.
  - **(c) A kafkajs consumer built from a variable is skipped.** `kafka.consumer(opts)` is skipped without a problem being logged, because the discovery regex requires `.consumer({ groupId`. It was caught here only because the workflow chart already has a block. A new service without a chart block would pass.
  - **(d) Other client libraries are not flagged.** Go `segmentio/kafka-go` and `confluent-kafka-go`, Python `confluent_kafka` and `aiokafka`, and other JS clients are not reported. None is in use today.
- **Fix:**
  - For TS, report any `subscribe({ topic: <literal> })` and any `.consumer(` call that the config object does not explain.
  - For Go, report a problem when `cfg.Group` or `cfg.Topics` is assigned after `ConfigFromEnv` with a value other than the default-group argument.
  - Extend the "consumer outside platform" scan to the other client libraries.

### 7. Nit: the pricing prefix ACL is safe today, but no test guards it
- **Where:** `deploy/kafka/pricing-consumer.yaml:26`
- **Finding:**
  - No other group ACL starts with `pricing-cache-`, and no other user holds a group prefix grant. I grepped every `type: group` ACL in `deploy/kafka`.
  - Nothing stops a future user from getting a literal group `pricing-cache-something`, which `pricing-consumer` could then join and whose offsets it could read and commit.
- **Fix:** in `test_each_consumer_block_has_a_least_privilege_kafka_user`, assert that no other user's group ACL starts with a granted prefix.

### 8. Nit: one failed produce aborts the smoke test
- **Where:** `deploy/scripts/smoke_consumers.py:151`
- **Finding:** `kube.sh` uses `check=True`. Producing to a topic that does not exist yet (see finding 1) raises an error and aborts the whole run, instead of recording a FAIL for that row. The script was not run, so this comes from reading the code.
- **Fix:** catch the error per row and record it as a FAIL.

### 9. Info: residual risk of the shared namespace (no change required)
- The SecretStore is namespaced, and its ServiceAccount has `automountServiceAccountToken: false`. External Secrets gets a short-lived token through the TokenRequest API.
- No Role in the repo lets hello-ledger workloads create ExternalSecrets or `serviceaccounts/token`.
- Anyone who can create ExternalSecrets or pods in hello-ledger can read both copied passwords. They can already read the copied Secrets there, so there is no escalation today.
- Each consumer turned on later widens this to every writer in hello-ledger. If that matters, a `PushSecret` from `kafka`, or per-service namespaces, would keep the token out of hello-ledger.

## Checked and found correct
- **ACLs.**
  - All 22 consumer users use `scram-sha-512` with `simple` authorization.
  - All grants are literal, except pricing's group prefix.
  - No `All` operations, no cluster grants, and no wildcards.
  - The DLQs are Write+Describe with a KafkaTopic each.
  - I hand-checked receiving (3 groups, 3 topics, 3 DLQs, and the `cfg.Group` reassignments), scheduling (`KAFKA_ORDER_GROUP` through `orderGroup`), workflow-ts, integration-hub, pricing (`KAFKA_GROUP` ignored, `pricing-cache-<HOSTNAME>`) and prediction (kafka-python, `PREDICTION_KAFKA_GROUP`/`TOPICS`, no DLQ). All match their users.
  - Idempotent producers need no cluster ACL on Kafka 4.0, because Write on a topic is enough (KIP-679).
- **Credential copy path.**
  - The Role is `get` on three named Secrets in `kafka`, with no list or watch. The RoleBinding's only subject is the store's ServiceAccount.
  - The ExternalSecrets copy only `password` and `ca.crt`, with `creationPolicy: Owner`.
  - The ExternalSecret, SecretStore, KafkaUser and KafkaTopic validate against the CRD catalog schemas.
- **Receiving consumer in dev.**
  - Brokers point at `:9093`.
  - The CA and password files are mounted with `fsGroup: 65532`, and the image runs as uid 65532.
  - The groups match the ACLs.
  - `receiving-db` and `receiving-audit-seed` are the same Secrets the API reads.
  - The topics are relayed, and franz-go tolerates topics that do not exist yet.
- **Renames.** No reference to `integration-hub.dlq` or to the old username remains in code, charts or tests. The hub's code takes the DLQ from `HUB_DLQ_TOPIC`, which the chart sets. No old KafkaUser existed to migrate.
- **Pricing chart.**
  - The password and CA are mounted as files with the TLS CA.
  - The `volumes` block is the pod's only one.
  - `fsGroup: 65532` makes the files readable whatever uid the image runs as. There is no Dockerfile in-tree to confirm the uid, but fsGroup does not depend on it.
  - `pricing-cache` is seeded in `serviceprincipal.go` and `data.json`, as the NOT-IMPLEMENTED update says.
- **Stage.**
  - `drop_dev_consumers` removes `consumer.kafka.{brokers,secret}` and `consumer.enabled` from `receiving-stage`. I checked the generated file.
  - The stage ApplicationSet reads `deploy/envs/stage/values/workflow.yaml`, which sets only `transportUrl`.
  - `stage_apps.py --check` passes.

## Tests run
With `PATH=/root/go/bin:/usr/local/go/bin:/root/.local/bin:$PATH`, in the worktree:
- **Unit tests** (all pass):

  | Test file | Result |
  |---|---|
  | `test_kafka_consumers.py` | 7 tests OK |
  | `test_argocd_apps.py` | 49 tests OK |
  | `test_migrate_jobs.py` | 12 tests OK |

- **Generator checks:** `gen_httproutes.py --check`, `gen_migrate_jobs.py --check`, `gen_networkpolicies.py --check` and `stage_apps.py --check` all exit 0.
- **Helm renders:** each was piped to `kubeconform -strict -ignore-missing-schemas -kubernetes-version 1.30.0`, and all were valid.
  - pricing, with defaults and with `cacheConsumer` on
  - integration-hub, with `kafka.*` set
  - receiving, with the dev `valuesObject`
  - workflow, with the dev values files and with the stage values file
- **CRD manifests:** `kubeconform` with the datreeio CRDs-catalog schemas validated `credentials.yaml`, `consumer-credentials-reader.yaml`, `pricing-consumer.yaml` and `integration-hub-consumer.yaml`: 10/10 valid.
- **Mutations** (scratch copy made with `git archive`):
  - An extra kafkajs subscribe slipped.
  - A Go `cfg.Group` override slipped.
  - `kafka.consumer(opts)` was caught, but only through the chart block.
  - An unrestricted `get secrets` Role rule slipped.
- **Not run:** `smoke_consumers.py`, because there is no cluster.
