# Release regression on dev

This is the durable replacement for the old temporary 97-check runner. It follows the user journeys in Foreman artefact 34 and the release gates in FPL283. Record every run and the exact serving versions in FPL300. A prior green run does not cover a newer build.

## Automated live checks

Materialize both dev PATs and the **dev** Kubernetes kubeconfig through Foreman. Download the candidate CLI release asset. Run `scripts/release_regression.py --help` and supply the current versions, three dev cluster IDs, credential file paths and a report path outside the repo. The runner refuses a non-dev API URL. It checks CLI, API, MCP and direct read-only Kubernetes state; it never writes cluster state or logs credentials. Store the secret-free JSON result with FPL300.

## Live user journeys

Run these on resources created for this pass. Record CLI/API/MCP/Console results and the live state, then delete through RunOS. A return code or completed job alone is insufficient.

1. **Account and versions:** In the Console switch between two dev accounts. Compare modules and cluster visibility with the CLI, API and MCP read server. Try anonymous and cross-account reads. Inspect `clusters k8s-versions`: 1.33 retired, 1.35 default, 1.36 offered. Try an invalid and retired service version; verify the refusal happens before a record or job appears.
2. **Service lifecycle:** Create a small Valkey on a disposable cluster, write a key, inspect health, request a patch-line change without data-loss acceptance, then with it. Verify the promised data loss and delete it. Create a PostgreSQL, write a marker, configure a backup, wait for an actual restorable point, restore to a new instance, read the marker, then delete both instances, schedule and destination through RunOS. Check a service status against its workloads with read-only `kubectl` where reachable.
3. **Cluster and node:** On a disposable lagging cluster, run patch preflight and roll one patch at a time. Compare the CLI, API and Console status with Kubernetes node versions, Ready and schedulable conditions, and Cilium pods. Refuse a skipped minor and verify no desired-version change. Keep every lab box powered on.
4. **VM and GPU Capacity:** Create an isolation group and a VM on a physical dev VM host; verify Running and the priority class with RunOS and Kubernetes, then delete it with its disk. For FCR869, build a disposable setup where Capacity is enabled but the `runos-on-demand` priority class is absent **using RunOS operations**. A VM create must return `409 vm.priority_class_missing` before an ID or VM record is saved. Repair through `capacity enable`, create a healthy VM, and remove all owned resources. Exercise a GPU inference pool only when a container GPU is actually available; otherwise record the hardware limitation, not a product pass.
5. **Bare metal and Console:** Read the provider and tenant views through their own accounts and confirm isolation. Do not deallocate, wipe or power off a standing lab box. In the signed-in Console check modules, service versions, cluster patch status, VM status and the displayed release. Capture discrepancies as FCRs.

Run independent repo gates and repeat affected journeys after any test-first fix. Merge and deploy fixes only to dev through Foreman. Final agent releases, key rotation and all prod actions belong to the operator.
