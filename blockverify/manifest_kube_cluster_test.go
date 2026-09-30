package main

// manifest_kube_cluster_test.go pins that the manifest suite passes a block
// providing kube-cluster, which declares eight outputs, and a block that
// requires kube-cluster and reads the four fields of its provided contract.

import "testing"

func TestManifestSuite_TheKubeClusterBlocksEightOutputsPass(t *testing.T) {
	dir := manifestDir(t, manifestHead+`  outputs:
    - privateEndpoint
    - issuerUrl
    - issuerBucket
    - clusterRepoUrl
    - serverId
    - cloud.region
    - host.type
    - enrolmentTokenSha256
`)
	requireManifestPass(t, manifestSuite(dir, ""))
}

func TestManifestSuite_AConsumerReadingTheKubeClusterContractFieldsPasses(t *testing.T) {
	dir := manifestDir(t, manifestHead+`  requires:
    - capability: kube-cluster
      reads: [privateEndpoint, issuerUrl, issuerBucket, clusterRepoUrl]
`)
	requireManifestPass(t, manifestSuite(dir, ""))
}
