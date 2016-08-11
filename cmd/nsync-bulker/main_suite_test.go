package main_test

import (
	"encoding/json"
	"net/url"
	"testing"

	"github.com/cloudfoundry-incubator/consuladapter/consulrunner"
	. "github.com/onsi/ginkgo"
	"github.com/onsi/ginkgo/config"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"
)

var (
	bulkerPath string

	k8sURL *url.URL

	consulRunner *consulrunner.ClusterRunner
)

func TestBulker(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Bulker Suite")
}

var _ = SynchronizedBeforeSuite(func() []byte {
	bulker, err := gexec.Build("github.com/cloudfoundry-incubator/nsync/cmd/nsync-bulker", "-race")
	Expect(err).NotTo(HaveOccurred())

	payload, err := json.Marshal(map[string]string{
		"bulker": bulker,
	})
	Expect(err).NotTo(HaveOccurred())

	return payload
}, func(payload []byte) {
	binaries := map[string]string{}

	err := json.Unmarshal(payload, &binaries)
	Expect(err).NotTo(HaveOccurred())

	consulRunner = consulrunner.NewClusterRunner(
		9001+config.GinkgoConfig.ParallelNode*consulrunner.PortOffsetLength,
		1,
		"http",
	)

	bulkerPath = string(binaries["bulker"])
})

var _ = BeforeEach(func() {
	consulRunner.Start()
	consulRunner.WaitUntilReady()
})

var _ = AfterEach(func() {
	consulRunner.Stop()
})

var _ = SynchronizedAfterSuite(func() {
}, func() {
	gexec.CleanupBuildArtifacts()
})
