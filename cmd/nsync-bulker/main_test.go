package main_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/api/resource"
	"k8s.io/kubernetes/pkg/api/v1"
	"k8s.io/kubernetes/pkg/client/restclient"
	"k8s.io/kubernetes/pkg/labels"

	"github.com/nu7hatch/gouuid"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/ghttp"
	"github.com/pivotal-golang/lager"
	"github.com/pivotal-golang/lager/lagertest"
	"github.com/tedsuo/ifrit"
	"github.com/tedsuo/ifrit/ginkgomon"

	"github.com/cloudfoundry-incubator/nsync/helpers"
	"github.com/cloudfoundry-incubator/runtime-schema/cc_messages"
	clientset "k8s.io/kubernetes/pkg/client/clientset_generated/release_1_3"

	v1core "k8s.io/kubernetes/pkg/client/clientset_generated/release_1_3/typed/core/v1"
	"k8s.io/kubernetes/pkg/client/unversioned/clientcmd"
)

var _ = Describe("Syncing desired state with CC", func() {
	const interruptTimeout = 5 * time.Second

	var (
		fakeCC *ghttp.Server

		process ifrit.Process

		domainTTL time.Duration

		//bulkerLockName    = "nsync_bulker_lock"
		pollingInterval time.Duration
		//heartbeatInterval time.Duration

		logger            lager.Logger
		excessPG          string
		newPG             string
		excessProcessGuid helpers.ProcessGuid
		newProcessGuid    helpers.ProcessGuid

		k8sClient v1core.CoreInterface

		home      = os.Getenv("HOME")
		config, _ = clientcmd.LoadFromFile(filepath.Join(home, ".kube", "config"))

		context = config.Contexts[config.CurrentContext]
	)

	startBulker := func(check bool) ifrit.Process {
		runner := ginkgomon.New(ginkgomon.Config{
			Name:          "nsync-bulker",
			AnsiColorCode: "97m",
			StartCheck:    "nsync.bulker.started",
			Command: exec.Command(
				bulkerPath,
				"-ccBaseURL", fakeCC.URL(),
				"-pollingInterval", pollingInterval.String(),
				"-domainTTL", domainTTL.String(),
				"-bulkBatchSize", "10",
				"-lifecycle", "buildpack/some-stack:some-health-check.tar.gz",
				"-lifecycle", "docker:the/docker/lifecycle/path.tgz",
				"-fileServerURL", "http://file-server.com",
				"-lockRetryInterval", "1s",
				"-consulCluster", consulRunner.ConsulCluster(),
				"-kubeCluster", config.Clusters[context.Cluster].Server,
				"-kubeClientCert", config.AuthInfos[context.AuthInfo].ClientCertificate,
				"-kubeClientKey", config.AuthInfos[context.AuthInfo].ClientKey,
				"-kubeCACert", config.Clusters[context.Cluster].CertificateAuthority,
				"-privilegedContainers", "false",
			),
		})

		if !check {
			runner.StartCheck = ""
		}

		return ginkgomon.Invoke(runner)
	}

	BeforeEach(func() {
		logger = lagertest.NewTestLogger("test")

		config := loadClientConfig()
		k8sConfig, err := clientset.NewForConfig(config)

		if err != nil {
			logger.Fatal("Can't create Kubernetes Client", err)
		}

		k8sClient = k8sConfig.Core()

		fakeCC = ghttp.NewServer()

		pollingInterval = 500 * time.Millisecond
		domainTTL = 1 * time.Second
		//heartbeatInterval = 30 * time.Second

		newPG = generateProcessGuid()
		excessPG = generateProcessGuid()

		newProcessGuid, err = helpers.NewProcessGuid(newPG)
		Expect(err).NotTo(HaveOccurred())
		excessProcessGuid, err = helpers.NewProcessGuid(excessPG)
		Expect(err).NotTo(HaveOccurred())

		desiredAppResponses := map[string]string{
			newPG: `{
					"disk_mb": 512,
					"file_descriptors": 8,
					"num_instances": 4,
					"log_guid": "log-guid-3",
					"memory_mb": 128,
					"process_guid": "` + newPG + `",
					"routing_info": { "http_routes": [] },
					"droplet_uri": "source-url-3",
					"stack": "some-stack",
					"start_command": "start-command-3",
					"execution_metadata": "execution-metadata-3",
					"health_check_timeout_in_seconds": 123456,
					"etag": "3.1",
					"environment": [
					{
						"name": "VCAP_APPLICATION",
						"value": "{\"space_id\": \"default\"}"
					}]
				}`,
		}

		fakeCC.RouteToHandler("GET", "/internal/bulk/apps",
			ghttp.RespondWith(200, `{
					"token": {},
					"fingerprints": [
							{
								"process_guid": "`+newPG+`",
								"etag": "1.1"
							}
					]
				}`),
		)

		fakeCC.RouteToHandler("POST", "/internal/bulk/apps",
			http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				var processGuids []string
				decoder := json.NewDecoder(req.Body)
				err := decoder.Decode(&processGuids)
				Expect(err).NotTo(HaveOccurred())

				appResponses := make([]json.RawMessage, 0, len(processGuids))
				for _, processGuid := range processGuids {
					appResponses = append(appResponses, json.RawMessage(desiredAppResponses[processGuid]))
				}

				payload, err := json.Marshal(appResponses)
				Expect(err).NotTo(HaveOccurred())

				w.Write(payload)
			}),
		)
	})

	AfterEach(func() {
		defer fakeCC.Close()

		// clean up the new rc in kube
		deleteReplicationController(k8sClient, "default", newProcessGuid)
	})

	Describe("when the CC polling interval elapses", func() {
		JustBeforeEach(func() {
			process = startBulker(true)
		})

		AfterEach(func() {
			ginkgomon.Interrupt(process, interruptTimeout)
		})

		Context("once the state has been synced with CC", func() {
			Context("lrps", func() {
				BeforeEach(func() {
					excessRC := &v1.ReplicationController{
						ObjectMeta: v1.ObjectMeta{
							Name:      excessProcessGuid.ShortenedGuid(),
							Namespace: "default",
							Labels: map[string]string{
								"cloudfoundry.org/process-guid": excessProcessGuid.ShortenedGuid(),
								"cloudfoundry.org/domain":       cc_messages.AppLRPDomain,
							},
							Annotations: map[string]string{
								"cloudfoundry.org/log-guid":      "the-log-guid",
								"cloudfoundry.org/metrics-guid":  "the-metrics-guid",
								"cloudfoundry.org/storage-space": "storage-space",
								"cloudfoundry.org/etag":          "current-etag",
							},
						},
						Spec: v1.ReplicationControllerSpec{
							Replicas: helpers.Int32Ptr(2),
							Selector: map[string]string{
								"cloudfoundry.org/process-guid": excessProcessGuid.ShortenedGuid(),
							},
							Template: &v1.PodTemplateSpec{
								ObjectMeta: v1.ObjectMeta{
									Labels: map[string]string{
										"cloudfoundry.org/app-guid":     excessProcessGuid.AppGuid.String(),
										"cloudfoundry.org/process-guid": excessProcessGuid.ShortenedGuid(),
										"cloudfoundry.org/domain":       cc_messages.AppLRPDomain,
									},
								},
								Spec: v1.PodSpec{
									Containers: []v1.Container{{
										Name:  "container-name",
										Image: "nginx",
										Resources: v1.ResourceRequirements{
											Limits: v1.ResourceList{
												v1.ResourceCPU:                   resource.MustParse("1000m"),
												v1.ResourceMemory:                resource.MustParse(fmt.Sprintf("%dMi", 256)),
												"cloudfoundry.org/storage-space": resource.MustParse(fmt.Sprintf("%dMi", 1024)),
											},
										},
									}},
								},
							},
						},
					}

					logger.Debug("creating the excess replication controller in kube", lager.Data{"excess": excessRC})

					rc, err := k8sClient.ReplicationControllers(excessRC.ObjectMeta.Namespace).Create(excessRC)
					Expect(err).NotTo(HaveOccurred())
					logger.Debug("created the excess replication controller in kube", lager.Data{"excess": rc})

				})

				// this test require a kubernetes env to point to, e.g. minikube
				PIt("it (adds), and (removes extra) LRPs", func() {
					Eventually(func() bool {
						rcList, _ := k8sClient.ReplicationControllers(api.NamespaceAll).List(api.ListOptions{
							LabelSelector: labels.Set{"cloudfoundry.org/process-guid": excessProcessGuid.ShortenedGuid()}.AsSelector(),
						})

						logger.Debug("rc List for excesss", lager.Data{"rclist": rcList})
						if len(rcList.Items) == 0 {
							return true // the excess rc is gone
						}
						return false
					}).Should(BeTrue())

					Eventually(func() bool {
						rcList, _ := k8sClient.ReplicationControllers(api.NamespaceAll).List(api.ListOptions{
							LabelSelector: labels.Set{"cloudfoundry.org/process-guid": newProcessGuid.ShortenedGuid()}.AsSelector(),
						})

						logger.Debug("rc List for new", lager.Data{"rclist": rcList})
						if len(rcList.Items) == 1 {
							return true // the new rc is created
						}
						return false
					}).Should(BeTrue())

				})
			})

		})
	})

})

func generateProcessGuid() string {
	appGuid, _ := uuid.NewV4()

	appVersion, _ := uuid.NewV4()

	return appGuid.String() + "-" + appVersion.String()
}

func loadClientConfig() *restclient.Config {
	home := os.Getenv("HOME")
	config, err := clientcmd.LoadFromFile(filepath.Join(home, ".kube", "config"))
	Expect(err).NotTo(HaveOccurred())

	context := config.Contexts[config.CurrentContext]

	clientConfig := &restclient.Config{
		Host:     config.Clusters[context.Cluster].Server,
		Username: config.AuthInfos[context.AuthInfo].Username,
		Password: config.AuthInfos[context.AuthInfo].Password,
		Insecure: config.Clusters[context.Cluster].InsecureSkipTLSVerify,
		TLSClientConfig: restclient.TLSClientConfig{
			CertFile: config.AuthInfos[context.AuthInfo].ClientCertificate,
			KeyFile:  config.AuthInfos[context.AuthInfo].ClientKey,
			CAFile:   config.Clusters[context.Cluster].CertificateAuthority,
		},
	}
	return clientConfig
}

func deleteReplicationController(k8sClient v1core.CoreInterface, namespace string, pg helpers.ProcessGuid) {
	k8sClient.ReplicationControllers(namespace).Delete(pg.ShortenedGuid(), nil)

	podsList, _ := k8sClient.Pods(namespace).List(api.ListOptions{
		LabelSelector: labels.Set{"cloudfoundry.org/process-guid": pg.ShortenedGuid()}.AsSelector(),
	})

	items := podsList.Items
	for _, pod := range items {
		k8sClient.Pods(namespace).Delete(pod.ObjectMeta.Name, nil)
	}
}
