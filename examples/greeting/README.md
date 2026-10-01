# Greeting example

A management-cluster API whose objects take effect in workload clusters.

- **`Greeting`** (`api/v1alpha1`) names a workload cluster and a message.
- **addon-manager** runs on the management cluster. It installs the greeting-controller package into each workload cluster.
- **greeting-syncer** runs on the management cluster. It copies each `Greeting` into the workload cluster it names.
- **greeting-controller** runs in each workload cluster. For each `Greeting`, it deploys `hello` behind nginx.
- **hello** serves the message.

`go run ./cmd/devenv test` brings up an environment, then:

1. creates a `Greeting`, and waits until the workload cluster serves its message,
2. updates the message, and waits for the new one,
3. redeploys with a test version, and waits until `hello` serves that version.

`cmd/devenv` holds this configuration. See the [repository README](../../README.md) for the other commands.
