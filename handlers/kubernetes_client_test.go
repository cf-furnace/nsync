package handlers_test

import "k8s.io/kubernetes/pkg/client/clientset_generated/release_1_3/typed/core/v1"

//go:generate counterfeiter -o fakes/fake_k8s_client.go --fake-name FakeKubeClient . kubeClient
type kubeClient interface {
	v1.CoreInterface
}

//go:generate counterfeiter -o fakes/fake_namespace.go --fake-name FakeNamespace . namespaceInterface
//go:generate sed -i .bak s,pkg/client/clientset_generated/release_1_3/typed/core/v1,pkg/api/v1,g fakes/fake_namespace.go
type namespaceInterface interface {
	v1.NamespaceInterface
}

//go:generate counterfeiter -o fakes/fake_replication_controller.go --fake-name FakeReplicationController . replicationControllerInterface
//go:generate sed -i .bak s,pkg/client/clientset_generated/release_1_3/typed/core/v1,pkg/api/v1,g fakes/fake_replication_controller.go
type replicationControllerInterface interface {
	v1.ReplicationControllerInterface
}
