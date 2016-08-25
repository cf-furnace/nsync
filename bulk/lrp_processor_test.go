package bulk_test

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/api/resource"
	"k8s.io/kubernetes/pkg/api/v1"

	"github.com/cloudfoundry-incubator/bbs/models"
	"github.com/cloudfoundry-incubator/nsync/bulk"
	"github.com/cloudfoundry-incubator/nsync/bulk/fakes"
	handlersfakes "github.com/cloudfoundry-incubator/nsync/handlers/fakes"
	"github.com/cloudfoundry-incubator/nsync/helpers"
	"github.com/cloudfoundry-incubator/nsync/recipebuilder"
	"github.com/cloudfoundry-incubator/runtime-schema/cc_messages"
	"github.com/cloudfoundry/dropsonde/metric_sender/fake"
	"github.com/cloudfoundry/dropsonde/metrics"
	"github.com/nu7hatch/gouuid"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/pivotal-golang/clock/fakeclock"
	"github.com/pivotal-golang/lager"
	"github.com/pivotal-golang/lager/lagertest"
	"github.com/tedsuo/ifrit"
	"github.com/tedsuo/ifrit/ginkgomon"
)

var _ = Describe("LRPProcessor", func() {
	var (
		fingerprintsToFetch []cc_messages.CCDesiredAppFingerprint
		//existingSchedulingInfos []*bulk.KubeSchedulingInfo

		fetcher   *fakes.FakeFetcher
		processor ifrit.Runner

		process      ifrit.Process
		syncDuration time.Duration
		metricSender *fake.FakeMetricSender
		clock        *fakeclock.FakeClock

		pollingInterval time.Duration

		logger                    *lagertest.TestLogger
		fakeKubeClient            *handlersfakes.FakeKubeClient
		fakeNamespace             *handlersfakes.FakeNamespace
		fakeReplicationController *handlersfakes.FakeReplicationController
		replicationControllerList *v1.ReplicationControllerList
		builders                  map[string]recipebuilder.FurnaceRecipeBuilder
		fakeBuilder               *fakes.FakeFurnaceRecipeBuilder
		currentProcessGuid        helpers.ProcessGuid
		currentPG                 string
		stalePG                   string
		dockerPG                  string
		excessPG                  string
		newPG                     string
		staleProcessGuid          helpers.ProcessGuid
		dockerProcessGuid         helpers.ProcessGuid
		excessProcessGuid         helpers.ProcessGuid
		newProcessGuid            helpers.ProcessGuid
	)

	BeforeEach(func() {
		logger = lagertest.NewTestLogger("test")

		metricSender = fake.NewFakeMetricSender()
		metrics.Initialize(metricSender, nil)

		appGuid, err := uuid.NewV4()
		Expect(err).NotTo(HaveOccurred())
		appVersion, err := uuid.NewV4()
		Expect(err).NotTo(HaveOccurred())

		currentPG = appGuid.String() + "-" + appVersion.String()
		currentProcessGuid, err = helpers.NewProcessGuid(currentPG)
		Expect(err).NotTo(HaveOccurred())
		stalePG = generateProcessGuid()
		dockerPG = generateProcessGuid()
		excessPG = generateProcessGuid()
		newPG = generateProcessGuid()

		staleProcessGuid, err = helpers.NewProcessGuid(stalePG)
		Expect(err).NotTo(HaveOccurred())
		dockerProcessGuid, err = helpers.NewProcessGuid(dockerPG)
		Expect(err).NotTo(HaveOccurred())
		excessProcessGuid, err = helpers.NewProcessGuid(excessPG)
		Expect(err).NotTo(HaveOccurred())
		newProcessGuid, err = helpers.NewProcessGuid(newPG)
		Expect(err).NotTo(HaveOccurred())

		syncDuration = 900900
		pollingInterval = 500 * time.Millisecond
		clock = fakeclock.NewFakeClock(time.Now())

		fingerprintsToFetch = []cc_messages.CCDesiredAppFingerprint{
			{ProcessGuid: currentPG, ETag: "current-etag"},
			{ProcessGuid: stalePG, ETag: "new-etag"},  //stake
			{ProcessGuid: dockerPG, ETag: "new-etag"}, //docker
			{ProcessGuid: newPG, ETag: "new-etag"},    //new
		}

		logger.Debug("fingerprints from CC", lager.Data{"fingerprints": fingerprintsToFetch})

		// tcpRouteInfo := cc_messages.CCTCPRoutes{
		// 	{
		// 		RouterGroupGuid: "router-group-guid",
		// 		ExternalPort:    11111,
		// 		ContainerPort:   9999,
		// 	},
		// }
		// tcpRoutesJson, err := json.Marshal(tcpRouteInfo)
		// Expect(err).NotTo(HaveOccurred())
		// staleTcpRouteMessage := json.RawMessage(tcpRoutesJson)

		//staleRouteMessage := json.RawMessage([]byte(`{ "some-route-key": "some-route-value" }`))
		// existingSchedulingInfos = []*bulk.KubeSchedulingInfo{
		// 	{
		// 		ProcessGuid: currentPG,
		// 		Annotation:  make(map[string]string),
		// 		Instances:   helpers.Int32Ptr(1),
		// 		ETag:        "current-etag",
		// 	},
		// 	{
		// 		ProcessGuid: stalePG,
		// 		Annotation:  make(map[string]string),
		// 		Instances:   helpers.Int32Ptr(1),
		// 		ETag:        "stale-etag",
		// 	},
		// 	{
		// 		ProcessGuid: dockerPG,
		// 		Annotation:  make(map[string]string),
		// 		Instances:   helpers.Int32Ptr(1),
		// 		ETag:        "docker-etag",
		// 	},
		// 	{
		// 		ProcessGuid: excessPG,
		// 		Annotation:  make(map[string]string),
		// 		Instances:   helpers.Int32Ptr(1),
		// 		ETag:        "excess-etag",
		// 	},
		// }

		fetcher = new(fakes.FakeFetcher)
		fetcher.FetchFingerprintsStub = func(
			logger lager.Logger,
			cancel <-chan struct{},
			httpClient *http.Client,
		) (<-chan []cc_messages.CCDesiredAppFingerprint, <-chan error) {
			results := make(chan []cc_messages.CCDesiredAppFingerprint, 1)
			errors := make(chan error, 1)

			results <- fingerprintsToFetch
			close(results)
			close(errors)

			return results, errors
		}

		fetcher.FetchDesiredAppsStub = func(
			logger lager.Logger,
			cancel <-chan struct{},
			httpClient *http.Client,
			fingerprints <-chan []cc_messages.CCDesiredAppFingerprint,
		) (<-chan []cc_messages.DesireAppRequestFromCC, <-chan error) {
			batch := <-fingerprints

			results := []cc_messages.DesireAppRequestFromCC{}
			for _, fingerprint := range batch {
				routeInfo, err := cc_messages.CCHTTPRoutes{
					{Hostname: "host-" + fingerprint.ProcessGuid},
				}.CCRouteInfo()
				Expect(err).NotTo(HaveOccurred())

				lrp := cc_messages.DesireAppRequestFromCC{
					ProcessGuid: fingerprint.ProcessGuid,
					ETag:        fingerprint.ETag,
					RoutingInfo: routeInfo,
				}
				if fingerprint.ProcessGuid == dockerPG {
					lrp.DockerImageUrl = "some-image"
				}
				results = append(results, lrp)
			}

			desired := make(chan []cc_messages.DesireAppRequestFromCC, 1)
			desired <- results
			close(desired)

			errors := make(chan error, 1)
			close(errors)

			return desired, errors
		}

		fakeBuilder = &fakes.FakeFurnaceRecipeBuilder{}
		builders = map[string]recipebuilder.FurnaceRecipeBuilder{
			"buildpack": fakeBuilder,
			"docker":    fakeBuilder,
		}

		fakeNamespace = &handlersfakes.FakeNamespace{}
		fakeNamespace.GetReturns(&v1.Namespace{
			ObjectMeta: v1.ObjectMeta{
				Name: "my-space-id",
			},
		}, nil)

		fakeReplicationController = &handlersfakes.FakeReplicationController{}
		fakeKubeClient = &handlersfakes.FakeKubeClient{}
		fakeKubeClient.NamespacesReturns(fakeNamespace)
		fakeKubeClient.ReplicationControllersReturns(fakeReplicationController)

		currentRC := &v1.ReplicationController{
			ObjectMeta: v1.ObjectMeta{
				Name:      currentProcessGuid.ShortenedGuid(),
				Namespace: "space-guid",
				Labels: map[string]string{
					"cloudfoundry.org/process-guid": currentProcessGuid.ShortenedGuid(),
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
				Replicas: helpers.Int32Ptr(999),
				Template: &v1.PodTemplateSpec{
					Spec: v1.PodSpec{
						Containers: []v1.Container{{
							Name: "container-name",
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

		staleRC := &v1.ReplicationController{
			ObjectMeta: v1.ObjectMeta{
				Name:      staleProcessGuid.ShortenedGuid(),
				Namespace: "space-guid",
				Labels: map[string]string{
					"cloudfoundry.org/process-guid": staleProcessGuid.ShortenedGuid(),
					"cloudfoundry.org/domain":       cc_messages.AppLRPDomain,
				},
				Annotations: map[string]string{
					"cloudfoundry.org/log-guid":      "the-log-guid",
					"cloudfoundry.org/metrics-guid":  "the-metrics-guid",
					"cloudfoundry.org/storage-space": "storage-space",
					"cloudfoundry.org/etag":          "stale-etag",
				},
			},
			Spec: v1.ReplicationControllerSpec{
				Replicas: helpers.Int32Ptr(999),
				Template: &v1.PodTemplateSpec{
					Spec: v1.PodSpec{
						Containers: []v1.Container{{
							Name: "container-name",
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

		dockerRC := &v1.ReplicationController{
			ObjectMeta: v1.ObjectMeta{
				Name:      dockerProcessGuid.ShortenedGuid(),
				Namespace: "space-guid",
				Labels: map[string]string{
					"cloudfoundry.org/process-guid": dockerProcessGuid.ShortenedGuid(),
					"cloudfoundry.org/domain":       cc_messages.AppLRPDomain,
				},
				Annotations: map[string]string{
					"cloudfoundry.org/log-guid":      "the-log-guid",
					"cloudfoundry.org/metrics-guid":  "the-metrics-guid",
					"cloudfoundry.org/storage-space": "storage-space",
					"cloudfoundry.org/etag":          "docker-etag",
				},
			},
			Spec: v1.ReplicationControllerSpec{
				Replicas: helpers.Int32Ptr(999),
				Template: &v1.PodTemplateSpec{
					Spec: v1.PodSpec{
						Containers: []v1.Container{{
							Name: "container-name",
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

		excessRC := &v1.ReplicationController{
			ObjectMeta: v1.ObjectMeta{
				Name:      excessProcessGuid.ShortenedGuid(),
				Namespace: "space-guid",
				Labels: map[string]string{
					"cloudfoundry.org/process-guid": excessProcessGuid.ShortenedGuid(),
					"cloudfoundry.org/domain":       cc_messages.AppLRPDomain,
				},
				Annotations: map[string]string{
					"cloudfoundry.org/log-guid":      "the-log-guid",
					"cloudfoundry.org/metrics-guid":  "the-metrics-guid",
					"cloudfoundry.org/storage-space": "storage-space",
					"cloudfoundry.org/etag":          "excess-etag",
				},
			},
			Spec: v1.ReplicationControllerSpec{
				Replicas: helpers.Int32Ptr(999),
				Template: &v1.PodTemplateSpec{
					Spec: v1.PodSpec{
						Containers: []v1.Container{{
							Name: "container-name",
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

		newRC := &v1.ReplicationController{
			ObjectMeta: v1.ObjectMeta{
				Name:      newProcessGuid.ShortenedGuid(),
				Namespace: "space-guid",
				Labels: map[string]string{
					"cloudfoundry.org/process-guid": newProcessGuid.ShortenedGuid(),
					"cloudfoundry.org/domain":       cc_messages.AppLRPDomain,
				},
				Annotations: map[string]string{
					"cloudfoundry.org/log-guid":      "the-log-guid",
					"cloudfoundry.org/metrics-guid":  "the-metrics-guid",
					"cloudfoundry.org/storage-space": "storage-space",
					"cloudfoundry.org/etag":          "new-etag",
				},
			},
			Spec: v1.ReplicationControllerSpec{
				Replicas: helpers.Int32Ptr(999),
				Template: &v1.PodTemplateSpec{
					Spec: v1.PodSpec{
						Containers: []v1.Container{{
							Name: "container-name",
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

		fakeBuilder.BuildReplicationControllerStub = func(desiredApp *cc_messages.DesireAppRequestFromCC) (*v1.ReplicationController, error) {
			if desiredApp.ProcessGuid == currentPG {
				return currentRC, nil
			} else if desiredApp.ProcessGuid == stalePG {
				return staleRC, nil
			} else if desiredApp.ProcessGuid == dockerPG {
				return dockerRC, nil
			} else if desiredApp.ProcessGuid == excessPG {
				return excessRC, nil
			} else {
				return newRC, nil
			}
		}
		replicationControllerList = &v1.ReplicationControllerList{
			Items: []v1.ReplicationController{*currentRC, *staleRC, *dockerRC, *excessRC},
		}
		//fakeReplicationController.ListReturns(replicationControllerList, nil)
		fakeReplicationController.ListStub = func(opts api.ListOptions) (*v1.ReplicationControllerList, error) {
			logger.Debug("print out opts", lager.Data{"opts": opts.LabelSelector.String()})
			if opts.LabelSelector.String() == "cloudfoundry.org/domain=cf-apps" {
				return replicationControllerList, nil
			} else {
				rcList := &v1.ReplicationControllerList{
					Items: []v1.ReplicationController{*excessRC},
				}
				return rcList, nil
			}
		}

		fakeReplicationController.UpdateStub = func(rc *v1.ReplicationController) (*v1.ReplicationController, error) {
			return rc, nil
		}
		fakeReplicationController.GetReturns(excessRC, nil)

		processor = bulk.NewLRPProcessor(
			logger,
			fakeKubeClient,
			500*time.Millisecond,
			time.Second,
			10,
			50,
			false,
			fetcher,
			builders,
			clock,
		)
	})

	JustBeforeEach(func() {
		process = ifrit.Invoke(processor)
	})

	AfterEach(func() {
		ginkgomon.Interrupt(process)
	})

	Context("when fetching succeeds", func() {
		Context("desired lrps", func() {
			Context("and the differ discovers desired LRPs to delete", func() {
				It("the processor deletes them", func() {
					Eventually(fakeReplicationController.DeleteCallCount).Should(Equal(1))
					Consistently(fakeReplicationController.DeleteCallCount).Should(Equal(1))
					name, _ := fakeReplicationController.DeleteArgsForCall(0)
					Expect(name).To(Equal(excessProcessGuid.ShortenedGuid()))
				})
			})

			Context("and the differ discovers missing apps and stale apps", func() {
				It("uses the recipe builder to construct the create and update LRP request", func() {
					Eventually(fakeBuilder.BuildReplicationControllerCallCount).Should(Equal(3))
					Consistently(fakeBuilder.BuildReplicationControllerCallCount).Should(Equal(3))

					Expect([]string{newPG, dockerPG, stalePG}).To(ConsistOf(
						fakeBuilder.BuildReplicationControllerArgsForCall(0).ProcessGuid,
						fakeBuilder.BuildReplicationControllerArgsForCall(1).ProcessGuid,
						fakeBuilder.BuildReplicationControllerArgsForCall(2).ProcessGuid))
				})

				It("creates a desired LRP for the missing app", func() {
					Eventually(fakeReplicationController.CreateCallCount).Should(Equal(1))
					Consistently(fakeReplicationController.CreateCallCount).Should(Equal(1))
					rc := fakeReplicationController.CreateArgsForCall(0)
					Expect(rc.ObjectMeta.Name).To(Equal(newProcessGuid.ShortenedGuid()))
				})

				It("updates the docker and stale LRP", func() {
					// delete call also adds an update count
					Eventually(fakeReplicationController.UpdateCallCount).Should(Equal(3))
					Consistently(fakeReplicationController.UpdateCallCount).Should(Equal(3))

					Expect([]string{dockerProcessGuid.ShortenedGuid(), staleProcessGuid.ShortenedGuid()}).To(ConsistOf(
						fakeReplicationController.UpdateArgsForCall(0).ObjectMeta.Name,
						fakeReplicationController.UpdateArgsForCall(1).ObjectMeta.Name))
				})

				Context("when fetching desire app requests from the CC fails", func() {
					BeforeEach(func() {
						fetcher.FetchDesiredAppsStub = func(
							logger lager.Logger,
							cancel <-chan struct{},
							httpClient *http.Client,
							fingerprints <-chan []cc_messages.CCDesiredAppFingerprint,
						) (<-chan []cc_messages.DesireAppRequestFromCC, <-chan error) {
							desireAppRequests := make(chan []cc_messages.DesireAppRequestFromCC)
							close(desireAppRequests)

							<-fingerprints

							err := errors.New("boom")
							logger.Error("errors when fetching desire app requests from CC", err)
							errorsChan := make(chan error, 1)
							errorsChan <- err
							close(errorsChan)

							return desireAppRequests, errorsChan
						}
					})

					It("keeps calm and carries on", func() {
						Consistently(process.Wait()).ShouldNot(Receive())
					})

					// It("does not update the domain", func() {
					// 	Consistently(bbsClient.UpsertDomainCallCount).Should(Equal(0))
					// })

					Context("and the differ provides creates, updates, and deletes", func() {
						It("sends the deletes but not the creates", func() {
							Consistently(fakeReplicationController.CreateCallCount).Should(Equal(0))
							// update is 1 because we update the RC replicas to 0
							Consistently(fakeReplicationController.UpdateCallCount).Should(Equal(1))
							Eventually(fakeReplicationController.DeleteCallCount).Should(Equal(1))
							Consistently(fakeReplicationController.DeleteCallCount).Should(Equal(1))
							desired, _ := fakeReplicationController.DeleteArgsForCall(0)
							Expect(desired).To(Equal(excessProcessGuid.ShortenedGuid()))
						})
					})
				})

				Context("when building the desire LRP request fails", func() {
					BeforeEach(func() {
						fakeBuilder.BuildReplicationControllerReturns(&v1.ReplicationController{}, errors.New("oh no failed to build RC"))
					})

					It("keeps calm and carries on", func() {
						Consistently(process.Wait()).ShouldNot(Receive())
					})

					// It("does not update the domain", func() {
					// 	Consistently(bbsClient.UpsertDomainCallCount).Should(Equal(0))
					// })

					Context("and the differ provides creates, updates, and deletes", func() {
						It("continues to send the deletes but not create", func() {
							Eventually(fakeReplicationController.DeleteCallCount).Should(Equal(1))
							Consistently(fakeReplicationController.DeleteCallCount).Should(Equal(1))

							desired, _ := fakeReplicationController.DeleteArgsForCall(0)
							Expect(desired).To(Equal(excessProcessGuid.ShortenedGuid()))

							Eventually(fakeReplicationController.UpdateCallCount).Should(Equal(1))
							Consistently(fakeReplicationController.UpdateCallCount).Should(Equal(1))

							Eventually(fakeReplicationController.CreateCallCount).Should(Equal(0))
							Consistently(fakeReplicationController.CreateCallCount).Should(Equal(0))
						})
					})
				})

				Context("when creating the missing desired LRP fails", func() {
					BeforeEach(func() {
						fakeReplicationController.CreateReturns(&v1.ReplicationController{}, errors.New("nope"))
					})

					It("keeps calm and carries on", func() {
						Consistently(process.Wait()).ShouldNot(Receive())
					})

					// It("does not update the domain", func() {
					// 	Consistently(bbsClient.UpsertDomainCallCount).Should(Equal(0))
					// })

					Context("and the differ provides creates, updates, and deletes", func() {
						It("continues to send the deletes and updates", func() {
							Eventually(fakeReplicationController.DeleteCallCount).Should(Equal(1))
							Consistently(fakeReplicationController.DeleteCallCount).Should(Equal(1))

							desired, _ := fakeReplicationController.DeleteArgsForCall(0)
							Expect(desired).To(Equal(excessProcessGuid.ShortenedGuid()))

							Eventually(fakeReplicationController.UpdateCallCount).Should(Equal(3))
							Consistently(fakeReplicationController.UpdateCallCount).Should(Equal(3))

							desired1 := fakeReplicationController.UpdateArgsForCall(0)
							desired2 := fakeReplicationController.UpdateArgsForCall(1)
							Expect([]string{desired1.ObjectMeta.Name, desired2.ObjectMeta.Name}).To(
								ConsistOf(staleProcessGuid.ShortenedGuid(), dockerProcessGuid.ShortenedGuid()))
						})
					})
				})
			})

			Context("and the differ provides creates and deletes", func() {
				It("sends them to the kube correctly", func() {
					Eventually(fakeReplicationController.CreateCallCount).Should(Equal(1))
					Eventually(fakeReplicationController.DeleteCallCount).Should(Equal(1))

					desire := fakeReplicationController.CreateArgsForCall(0)
					Expect(desire.ObjectMeta.Name).To(Equal(newProcessGuid.ShortenedGuid()))
					Expect(desire.ObjectMeta.Annotations["cloudfoundry.org/etag"]).To(Equal("new-etag"))

					removed, _ := fakeReplicationController.DeleteArgsForCall(0)
					Expect(removed).To(Equal(excessProcessGuid.ShortenedGuid()))
				})

				Context("and the create request fails", func() {
					BeforeEach(func() {
						fakeReplicationController.CreateReturns(&v1.ReplicationController{}, errors.New("create failed!"))
					})

					// It("does not update the domain", func() {
					// 	Consistently(bbsClient.UpsertDomainCallCount).Should(Equal(0))
					// })

					It("sends all the other updates", func() {
						Eventually(fakeReplicationController.CreateCallCount).Should(Equal(1))
						Eventually(fakeReplicationController.DeleteCallCount).Should(Equal(1))
					})
				})

				Context("and the delete request fails", func() {
					BeforeEach(func() {
						fakeReplicationController.DeleteReturns(errors.New("delete failed!"))
					})

					It("sends all the other updates", func() {
						Eventually(fakeReplicationController.CreateCallCount).Should(Equal(1))
						Eventually(fakeReplicationController.DeleteCallCount).Should(Equal(1))
					})
				})
			})

			Context("and the differ detects stale lrps", func() {
				var (
					expectedInstances = int32(999)

					expectedRouteHost string
					expectedPort      uint32
					//expectedRoutingInfo *models.Routes

					expectedClientCallCount int

					processGuids []string
					rcs          []*v1.ReplicationController
				)

				BeforeEach(func() {
					expectedPort = 8080
					expectedRouteHost = "host-stale-process-guid"
					expectedClientCallCount = 3
				})

				JustBeforeEach(func() {
					Eventually(fakeReplicationController.UpdateCallCount).Should(Equal(expectedClientCallCount))

					// opaqueRouteMessage := json.RawMessage([]byte(`{ "some-route-key": "some-route-value" }`))
					// cfRoute := cfroutes.CFRoutes{
					// 	{Hostnames: []string{expectedRouteHost}, Port: expectedPort},
					// }
					// cfRoutePayload, err := json.Marshal(cfRoute)
					// Expect(err).NotTo(HaveOccurred())
					// cfRouteMessage := json.RawMessage(cfRoutePayload)
					//
					// tcpRouteMessage := json.RawMessage([]byte(`[]`))
					//
					// expectedRoutingInfo = &models.Routes{
					// 	"router-route-data":   &opaqueRouteMessage,
					// 	cfroutes.CF_ROUTER:    &cfRouteMessage,
					// 	tcp_routes.TCP_ROUTER: &tcpRouteMessage,
					// }

					for i := 0; i < expectedClientCallCount; i++ {
						rc := fakeReplicationController.UpdateArgsForCall(i)
						processGuids = append(processGuids, rc.ObjectMeta.Name)
						rcs = append(rcs, rc)
					}
				})

				It("sends the correct update desired lrp request", func() {
					Expect(processGuids).To(ContainElement(staleProcessGuid.ShortenedGuid()))
					Expect(processGuids).To(ContainElement(dockerProcessGuid.ShortenedGuid()))
					Expect(processGuids).To(ContainElement(excessProcessGuid.ShortenedGuid()))

					for i := 0; i < expectedClientCallCount; i++ {
						Expect([]string{dockerProcessGuid.ShortenedGuid(), staleProcessGuid.ShortenedGuid(), excessProcessGuid.ShortenedGuid()}).To(
							ContainElement(rcs[i].ObjectMeta.Name))
						if rcs[i].ObjectMeta.Name == dockerProcessGuid.ShortenedGuid() || rcs[i].ObjectMeta.Name == staleProcessGuid.ShortenedGuid() {
							Expect(rcs[i].Spec.Replicas).To(Equal(&expectedInstances))
						} else {
							instance := int32(0)
							Expect(rcs[i].Spec.Replicas).To(Equal(&instance))
						}
					}

				})

			})

			Context("when updating the desired lrp fails", func() {
				BeforeEach(func() {
					fakeReplicationController.UpdateReturns(&v1.ReplicationController{}, errors.New("boom"))
				})

				// Context("because the desired lrp is invalid", func() {
				// 	BeforeEach(func() {
				// 		validationError := models.NewError(models.Error_InvalidRequest, "some-validation-error")
				// 		fakeReplicationController.UpdateReturns(&v1.ReplicationController{}, validationError.ToError())
				// 	})
				//
				// 	It("updates the domain", func() {
				// 	 	Eventually(bbsClient.UpsertDomainCallCount).Should(Equal(1))
				// 	})
				//
				// 	It("correctly emits the total number of invalid LRPs found while bulking", func() {
				// 		Eventually(func() fake.Metric {
				// 			return metricSender.GetValue("NsyncInvalidDesiredLRPsFound")
				// 		}).Should(Equal(fake.Metric{Value: 2, Unit: "Metric"}))
				// 	})
				// })

				// It("does not update the domain", func() {
				// 	Consistently(bbsClient.UpsertDomainCallCount).Should(Equal(0))
				// })

				It("still sends create request, delete/update will be skipped", func() {
					Eventually(fakeReplicationController.CreateCallCount).Should(Equal(1))
				})
			})

			Context("when creating the desired lrp fails", func() {
				BeforeEach(func() {
					fakeReplicationController.CreateReturns(&v1.ReplicationController{}, errors.New("boom"))
				})

				Context("because the desired lrp is invalid", func() {
					BeforeEach(func() {
						validationError := models.NewError(models.Error_InvalidRequest, "some-validation-error")
						fakeReplicationController.CreateReturns(&v1.ReplicationController{}, validationError.ToError())
					})

					// It("updates the domain", func() {
					// 	Eventually(bbsClient.UpsertDomainCallCount).Should(Equal(1))
					// })

					It("correctly emits the total number of invalid LRPs found while bulking", func() {
						Eventually(func() fake.Metric {
							return metricSender.GetValue("NsyncInvalidDesiredLRPsFound")
						}).Should(Equal(fake.Metric{Value: 1, Unit: "Metric"}))
					})
				})

				// It("does not update the domain", func() {
				// 	Consistently(bbsClient.UpsertDomainCallCount).Should(Equal(0))
				// })

				It("sends all the other updates", func() {
					Eventually(fakeReplicationController.CreateCallCount).Should(Equal(1))
					Eventually(fakeReplicationController.DeleteCallCount).Should(Equal(1))
				})
			})
		})
	})

	Context("when getting all desired LRPs fails", func() {
		BeforeEach(func() {
			fakeReplicationController.ListReturns(&v1.ReplicationControllerList{}, errors.New("oh no!"))
		})

		It("keeps calm and carries on", func() {
			Consistently(process.Wait()).ShouldNot(Receive())
		})

		It("tries again after the polling interval", func() {
			Eventually(fakeReplicationController.ListCallCount).Should(Equal(1))
			clock.Increment(pollingInterval / 2)
			Consistently(fakeReplicationController.ListCallCount).Should(Equal(1))

			clock.Increment(pollingInterval)
			Eventually(fakeReplicationController.ListCallCount).Should(Equal(2))
		})

		It("does not call the differ, the fetcher, or the bbs client for updates", func() {
			Consistently(fetcher.FetchFingerprintsCallCount).Should(Equal(0))
			Consistently(fetcher.FetchDesiredAppsCallCount).Should(Equal(0))
			Consistently(fakeBuilder.BuildReplicationControllerCallCount()).Should(Equal(0))
			Consistently(fakeReplicationController.CreateCallCount).Should(Equal(0))
			Consistently(fakeReplicationController.DeleteCallCount).Should(Equal(0))
			Consistently(fakeReplicationController.UpdateCallCount).Should(Equal(0))
			//Consistently(bbsClient.UpsertDomainCallCount).Should(Equal(0))
		})
	})

	Context("when fetching fingerprints fails", func() {
		BeforeEach(func() {
			fetcher.FetchFingerprintsStub = func(
				logger lager.Logger,
				cancel <-chan struct{},
				httpClient *http.Client,
			) (<-chan []cc_messages.CCDesiredAppFingerprint, <-chan error) {
				results := make(chan []cc_messages.CCDesiredAppFingerprint, 1)
				errorsChan := make(chan error, 1)

				results <- fingerprintsToFetch
				close(results)

				errorsChan <- errors.New("uh oh")
				close(errorsChan)

				return results, errorsChan
			}
		})

		It("keeps calm and carries on", func() {
			Consistently(process.Wait()).ShouldNot(Receive())
		})

		// It("does not update the domain", func() {
		// 	Consistently(bbsClient.UpsertDomainCallCount).Should(Equal(0))
		// })

		It("sends the creates and updates for the apps it got but not the deletes", func() {
			Eventually(fakeReplicationController.CreateCallCount).Should(Equal(1))
			Eventually(fakeReplicationController.UpdateCallCount).Should(Equal(2))
			Consistently(fakeReplicationController.DeleteCallCount).Should(Equal(0))
		})
	})
})

func generateProcessGuid() string {
	appGuid, _ := uuid.NewV4()

	appVersion, _ := uuid.NewV4()

	return appGuid.String() + "-" + appVersion.String()
}
