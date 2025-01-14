apiVersion: manifests.package-operator.run/v1alpha1
kind: PackageManifest
metadata:
  creationTimestamp: null
  name: remote-phase
spec:
  availabilityProbes:
  - probes:
    - condition:
        status: "True"
        type: Available
    - fieldsEqual:
        fieldA: .status.updatedReplicas
        fieldB: .status.replicas
    selector:
      kind:
        group: apps
        kind: Deployment
  config: {}
  phases:
  - name: rbac
  - name: deploy
  scopes:
  - Namespaced
test:
  template:
  - context:
      package:
        metadata:
          annotations: null
          labels: null
          name: test
          namespace: test-ns
    name: namespace-scope
