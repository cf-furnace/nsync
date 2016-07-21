package recipebuilder_test

import (
	"encoding/json"
	"fmt"
	"strings"

	"k8s.io/kubernetes/pkg/api/resource"
	"k8s.io/kubernetes/pkg/api/v1"

	"github.com/cloudfoundry-incubator/bbs/models"
	"github.com/cloudfoundry-incubator/diego-ssh/keys/fake_keys"
	"github.com/cloudfoundry-incubator/nsync/helpers"
	"github.com/cloudfoundry-incubator/nsync/recipebuilder"
	"github.com/cloudfoundry-incubator/routing-info/cfroutes"
	"github.com/cloudfoundry-incubator/routing-info/tcp_routes"
	"github.com/cloudfoundry-incubator/runtime-schema/cc_messages"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/pivotal-golang/lager/lagertest"
)

var _ = Describe("Buildpack Recipe Builder", func() {
	var (
		builder        *recipebuilder.BuildpackRecipeBuilder
		err            error
		desiredAppReq  cc_messages.DesireAppRequestFromCC
		lifecycles     map[string]string
		egressRules    []*models.SecurityGroupRule
		networkInfo    *models.Network
		fakeKeyFactory *fake_keys.FakeSSHKeyFactory
		logger         *lagertest.TestLogger
		expectedRoutes models.Routes
		processGuid    helpers.ProcessGuid
	)

	BeforeEach(func() {
		logger = lagertest.NewTestLogger("test")

		lifecycles = map[string]string{
			"buildpack/some-stack": "some-lifecycle.tgz",
			"docker":               "the/docker/lifecycle/path.tgz",
		}

		egressRules = []*models.SecurityGroupRule{
			{
				Protocol:     "TCP",
				Destinations: []string{"0.0.0.0/0"},
				PortRange:    &models.PortRange{Start: 80, End: 443},
			},
		}

		networkInfo = &models.Network{
			Properties: map[string]string{
				"app_id":   "some-app-guid",
				"some_key": "some-value",
			},
		}

		fakeKeyFactory = &fake_keys.FakeSSHKeyFactory{}
		config := recipebuilder.Config{
			Lifecycles:    lifecycles,
			FileServerURL: "http://file-server.com",
			KeyFactory:    fakeKeyFactory,
		}
		builder = recipebuilder.NewBuildpackRecipeBuilder(logger, config)

		routingInfo, err := cc_messages.CCHTTPRoutes{
			{Hostname: "route1"},
			{Hostname: "route2"},
		}.CCRouteInfo()
		Expect(err).NotTo(HaveOccurred())

		processGuid, err = helpers.NewProcessGuid("8d58c09b-b305-4f16-bcfe-b78edcb77100-3f258eb0-9dac-460c-a424-b43fe92bee27")
		Expect(err).NotTo(HaveOccurred())

		desiredAppReq = cc_messages.DesireAppRequestFromCC{
			ProcessGuid:       processGuid.String(),
			DropletUri:        "http://the-droplet.uri.com",
			DropletHash:       "some-hash",
			Stack:             "some-stack",
			StartCommand:      "the-start-command with-arguments",
			ExecutionMetadata: "the-execution-metadata",
			Environment: []*models.EnvironmentVariable{
				{Name: "foo", Value: "bar"},
				{Name: "VCAP_APPLICATION", Value: `{"space_id": "space-guid"}`},
			},
			MemoryMB:        128,
			DiskMB:          512,
			FileDescriptors: 32,
			NumInstances:    23,
			RoutingInfo:     routingInfo,
			LogGuid:         "the-log-id",
			LogSource:       "MYSOURCE",

			HealthCheckType:             cc_messages.PortHealthCheckType,
			HealthCheckTimeoutInSeconds: 123456,

			EgressRules: egressRules,
			Network:     networkInfo,

			ETag: "etag-updated-at",
		}

		cfRoutes := json.RawMessage([]byte(`[{"hostnames":["route1","route2"],"port":8080}]`))
		tcpRoutes := json.RawMessage([]byte("[]"))
		expectedRoutes = models.Routes{
			cfroutes.CF_ROUTER:    &cfRoutes,
			tcp_routes.TCP_ROUTER: &tcpRoutes,
		}
	})

	Describe("Build", func() {
		var replicationController *v1.ReplicationController

		JustBeforeEach(func() {
			replicationController, err = builder.BuildReplicationController(&desiredAppReq)
		})

		Context("when ports is an empty array", func() {
			BeforeEach(func() {
				desiredAppReq.Ports = []uint32{}
			})

			It("requests no ports on the lrp", func() {
				Expect(replicationController.Spec.Template.Spec.Containers[0].Ports).To(BeEmpty())
			})

			It("does not include a PORT environment variable", func() {
				varNames := []string{}
				for _, envVar := range replicationController.Spec.Template.Spec.Containers[0].Env {
					varNames = append(varNames, envVar.Name)
				}

				Expect(varNames).NotTo(ContainElement("PORT"))
			})
		})

		Context("when everything is correct", func() {
			It("does not error", func() {
				Expect(err).NotTo(HaveOccurred())
			})

			It("builds a valid DesiredLRP", func() {
				Expect(replicationController.ObjectMeta.Name).To(Equal(processGuid.ShortenedGuid()))
				Expect(replicationController.ObjectMeta.Namespace).To(Equal("space-guid"))

				Expect(replicationController.ObjectMeta.Labels).To(HaveKeyWithValue("cloudfoundry.org/app-guid", processGuid.AppGuid.String()))
				Expect(replicationController.ObjectMeta.Labels).To(HaveKeyWithValue("cloudfoundry.org/space-guid", "space-guid"))
				Expect(replicationController.ObjectMeta.Labels).To(HaveKeyWithValue("cloudfoundry.org/process-guid", processGuid.ShortenedGuid()))

				Expect(replicationController.ObjectMeta.Annotations).To(HaveKeyWithValue("cloudfoundry.org/allow-ssh", "false"))
				Expect(replicationController.ObjectMeta.Annotations).To(HaveKeyWithValue("cloudfoundry.org/routing-info", MustEncode(expectedRoutes)))
				Expect(replicationController.ObjectMeta.Annotations).To(HaveKeyWithValue("cloudfoundry.org/egress-rules", MustEncode(egressRules)))
				Expect(replicationController.ObjectMeta.Annotations).To(HaveKeyWithValue("cloudfoundry.org/log-source", "MYSOURCE"))
				Expect(replicationController.ObjectMeta.Annotations).To(HaveKeyWithValue("cloudfoundry.org/log-guid", "the-log-id"))
				Expect(replicationController.ObjectMeta.Annotations).To(HaveKeyWithValue("cloudfoundry.org/metrics-guid", "the-log-id"))

				Expect(*replicationController.Spec.Replicas).To(BeEquivalentTo(23))
				Expect(replicationController.Spec.Selector).To(Equal(map[string]string{"cloudfoundry.org/process-guid": processGuid.ShortenedGuid()}))

				Expect(replicationController.Spec.Template.Spec.Volumes).To(ConsistOf(
					v1.Volume{
						Name:         "lifecycle",
						VolumeSource: v1.VolumeSource{EmptyDir: &v1.EmptyDirVolumeSource{}},
					},
					v1.Volume{
						Name:         "droplet",
						VolumeSource: v1.VolumeSource{EmptyDir: &v1.EmptyDirVolumeSource{}},
					},
				))

				// Monitoring
				// Expect(desiredLRP.Annotation).To(Equal("etag-updated-at"))
				// Expect(desiredLRP.LogSource).To(Equal("CELL"))
				// Expect(desiredLRP.Network).To(Equal(networkInfo))
				// Expect(desiredLRP.TrustedSystemCertificatesPath).To(Equal(recipebuilder.TrustedSystemCertificatesPath))

				Expect(replicationController.Spec.Template.Spec.Containers).To(HaveLen(1))
				appContainer := replicationController.Spec.Template.Spec.Containers[0]

				Expect(appContainer.Name).To(Equal("application"))
				Expect(appContainer.Image).To(Equal("cloudfoundry/some-stack:latest"))
				Expect(appContainer.ImagePullPolicy).To(BeEquivalentTo("IfNotPresent"))

				Expect(appContainer.Resources.Limits).To(HaveKeyWithValue(v1.ResourceCPU, resource.MustParse("1000m")))
				Expect(appContainer.Resources.Limits).To(HaveKeyWithValue(v1.ResourceMemory, resource.MustParse("128Mi")))
				Expect(appContainer.Resources.Limits).To(HaveKeyWithValue(v1.ResourceName("cloudfoundry.org/storage-space"), resource.MustParse("512Mi")))
				Expect(appContainer.Resources.Limits).To(HaveKeyWithValue(v1.ResourceName("cloudfoundry.org/file-descriptors"), resource.MustParse("32")))

				Expect(appContainer.VolumeMounts).To(ContainElement(v1.VolumeMount{Name: "droplet", MountPath: "/home/vcap/app"}))
				Expect(appContainer.VolumeMounts).To(ContainElement(v1.VolumeMount{Name: "lifecycle", MountPath: "/tmp/lifecycle", ReadOnly: true}))

				Expect(appContainer.SecurityContext.Privileged).To(BeNil())
				Expect(appContainer.SecurityContext.RunAsUser).To(Equal(helpers.Int64Ptr(2000)))

				Expect(appContainer.Env).To(ConsistOf(
					v1.EnvVar{Name: "foo", Value: "bar"},
					v1.EnvVar{Name: "LANG", Value: recipebuilder.DefaultLANG},
					v1.EnvVar{Name: "PORT", Value: "8080"},
					v1.EnvVar{Name: "VCAP_APPLICATION", Value: `{"space_id": "space-guid"}`},
				))

				Expect(appContainer.Command).To(ConsistOf(
					"/bin/bash",
					"-c",
					`exec /tmp/lifecycle/launcher app "the-start-command with-arguments" "the-execution-metadata"`,
				))

				Expect(appContainer.Ports).To(ConsistOf(v1.ContainerPort{Protocol: v1.ProtocolTCP, ContainerPort: 8080}))

				initContainers := []v1.Container{}
				err := json.Unmarshal([]byte(replicationController.Spec.Template.Annotations[v1.PodInitContainersAnnotationKey]), &initContainers)
				Expect(err).NotTo(HaveOccurred())

				Expect(initContainers).To(ConsistOf(
					v1.Container{
						Name:  "setup",
						Image: "cloudfoundry/some-stack:latest",
						VolumeMounts: []v1.VolumeMount{
							{Name: "droplet", MountPath: "/droplet"},
							{Name: "lifecycle", MountPath: "/lifecycle"},
						},
						Env: []v1.EnvVar{
							{Name: "LIFECYCLE_URL", Value: "http://file-server.com/v1/static/some-lifecycle.tgz"},
							{Name: "DROPLET_URL", Value: "http://the-droplet.uri.com"},
						},
						SecurityContext: &v1.SecurityContext{
							RunAsUser: helpers.Int64Ptr(2000),
						},
						Command: []string{
							"/bin/bash",
							"-c",
							strings.Join([]string{
								"set -ex",
								"wget --no-check-certificate -O- $LIFECYCLE_URL | tar -zxv -C /lifecycle",
								"wget --no-check-certificate -O- $DROPLET_URL | tar -zxv -C /droplet",
							}, "\n"),
						},
					},
				))
			})

			Context("when route service url is specified in RoutingInfo", func() {
				BeforeEach(func() {
					routingInfo, err := cc_messages.CCHTTPRoutes{
						{Hostname: "route1"},
						{Hostname: "route2", RouteServiceUrl: "https://rs.example.com"},
					}.CCRouteInfo()
					Expect(err).NotTo(HaveOccurred())
					desiredAppReq.RoutingInfo = routingInfo
				})

				It("sets up routes with the route service url", func() {
					routes := models.Routes{}
					err := json.Unmarshal([]byte(replicationController.ObjectMeta.Annotations["cloudfoundry.org/routing-info"]), &routes)
					Expect(err).NotTo(HaveOccurred())

					cfRoutesJson := routes[cfroutes.CF_ROUTER]
					cfRoutes := cfroutes.CFRoutes{}

					err = json.Unmarshal(*cfRoutesJson, &cfRoutes)
					Expect(err).ToNot(HaveOccurred())

					Expect(cfRoutes).To(ConsistOf([]cfroutes.CFRoute{
						{Hostnames: []string{"route1"}, Port: 8080},
						{Hostnames: []string{"route2"}, Port: 8080, RouteServiceUrl: "https://rs.example.com"},
					}))
				})
			})

			// Context("when no health check is specified", func() {
			// 	BeforeEach(func() {
			// 		desiredAppReq.HealthCheckType = cc_messages.UnspecifiedHealthCheckType
			// 	})

			// 	It("sets up the port check for backwards compatibility", func() {
			// 		downloadDestinations := []string{}
			// 		for _, dep := range desiredLRP.CachedDependencies {
			// 			if dep != nil {
			// 				downloadDestinations = append(downloadDestinations, dep.To)
			// 			}
			// 		}

			// 		Expect(downloadDestinations).To(ContainElement("/tmp/lifecycle"))

			// 		Expect(desiredLRP.Monitor.GetValue()).To(Equal(models.Timeout(
			// 			&models.ParallelAction{
			// 				Actions: []*models.Action{
			// 					&models.Action{
			// 						RunAction: &models.RunAction{
			// 							User:      "vcap",
			// 							Path:      "/tmp/lifecycle/healthcheck",
			// 							Args:      []string{"-port=8080"},
			// 							LogSource: "HEALTH",
			// 							ResourceLimits: &models.ResourceLimits{
			// 								Nofile: &defaultNofile,
			// 							},
			// 							SuppressLogOutput: true,
			// 						},
			// 					},
			// 				},
			// 			},
			// 			30*time.Second,
			// 		)))
			// 	})
			// })

			// Context("when the 'none' health check is specified", func() {
			// 	BeforeEach(func() {
			// 		desiredAppReq.HealthCheckType = cc_messages.NoneHealthCheckType
			// 	})

			// 	It("does not populate the monitor action", func() {
			// 		Expect(desiredLRP.Monitor).To(BeNil())
			// 	})

			// 	It("still downloads the lifecycle, since we need it for the launcher", func() {
			// 		downloadDestinations := []string{}
			// 		for _, dep := range desiredLRP.CachedDependencies {
			// 			if dep != nil {
			// 				downloadDestinations = append(downloadDestinations, dep.To)
			// 			}
			// 		}

			// 		Expect(downloadDestinations).To(ContainElement("/tmp/lifecycle"))
			// 	})
			// })

			// Context("when allow ssh is true", func() {
			// 	BeforeEach(func() {
			// 		desiredAppReq.AllowSSH = true

			// 		keyPairChan := make(chan keys.KeyPair, 2)

			// 		fakeHostKeyPair := &fake_keys.FakeKeyPair{}
			// 		fakeHostKeyPair.PEMEncodedPrivateKeyReturns("pem-host-private-key")
			// 		fakeHostKeyPair.FingerprintReturns("host-fingerprint")

			// 		fakeUserKeyPair := &fake_keys.FakeKeyPair{}
			// 		fakeUserKeyPair.AuthorizedKeyReturns("authorized-user-key")
			// 		fakeUserKeyPair.PEMEncodedPrivateKeyReturns("pem-user-private-key")

			// 		keyPairChan <- fakeHostKeyPair
			// 		keyPairChan <- fakeUserKeyPair

			// 		fakeKeyFactory.NewKeyPairStub = func(bits int) (keys.KeyPair, error) {
			// 			return <-keyPairChan, nil
			// 		}
			// 	})

			// 	It("setup should download the ssh daemon", func() {
			// 		expectedCacheDependencies := []*models.CachedDependency{
			// 			{
			// 				From:     "http://file-server.com/v1/static/some-lifecycle.tgz",
			// 				To:       "/tmp/lifecycle",
			// 				CacheKey: "buildpack-some-stack-lifecycle",
			// 			},
			// 		}

			// 		Expect(desiredLRP.CachedDependencies).To(BeEquivalentTo(expectedCacheDependencies))
			// 	})

			// 	It("runs the ssh daemon in the container", func() {
			// 		expectedNumFiles := uint64(32)

			// 		expectedAction := models.Codependent(
			// 			&models.RunAction{
			// 				User: "vcap",
			// 				Path: "/tmp/lifecycle/launcher",
			// 				Args: []string{
			// 					"app",
			// 					"the-start-command with-arguments",
			// 					"the-execution-metadata",
			// 				},
			// 				Env: []*models.EnvironmentVariable{
			// 					{Name: "foo", Value: "bar"},
			// 					{Name: "PORT", Value: "8080"},
			// 				},
			// 				ResourceLimits: &models.ResourceLimits{
			// 					Nofile: &expectedNumFiles,
			// 				},
			// 				LogSource: "MYSOURCE",
			// 			},
			// 			&models.RunAction{
			// 				User: "vcap",
			// 				Path: "/tmp/lifecycle/diego-sshd",
			// 				Args: []string{
			// 					"-address=0.0.0.0:2222",
			// 					"-hostKey=pem-host-private-key",
			// 					"-authorizedKey=authorized-user-key",
			// 					"-inheritDaemonEnv",
			// 					"-logLevel=fatal",
			// 				},
			// 				Env: []*models.EnvironmentVariable{
			// 					{Name: "foo", Value: "bar"},
			// 					{Name: "PORT", Value: "8080"},
			// 				},
			// 				ResourceLimits: &models.ResourceLimits{
			// 					Nofile: &expectedNumFiles,
			// 				},
			// 			},
			// 		)

			// 		Expect(desiredLRP.Action.GetValue()).To(Equal(expectedAction))
			// 	})

			// 	It("opens up the default ssh port", func() {
			// 		Expect(desiredLRP.Ports).To(Equal([]uint32{
			// 			8080,
			// 			2222,
			// 		}))
			// 	})

			// 	It("declares ssh routing information in the LRP", func() {
			// 		cfRoutePayload, err := json.Marshal(cfroutes.CFRoutes{
			// 			{Hostnames: []string{"route1", "route2"}, Port: 8080},
			// 		})
			// 		Expect(err).NotTo(HaveOccurred())

			// 		sshRoutePayload, err := json.Marshal(routes.SSHRoute{
			// 			ContainerPort:   2222,
			// 			PrivateKey:      "pem-user-private-key",
			// 			HostFingerprint: "host-fingerprint",
			// 		})
			// 		Expect(err).NotTo(HaveOccurred())

			// 		cfRouteMessage := json.RawMessage(cfRoutePayload)
			// 		tcpRouteMessage := json.RawMessage([]byte("[]"))
			// 		sshRouteMessage := json.RawMessage(sshRoutePayload)

			// 		Expect(desiredLRP.Routes).To(Equal(&models.Routes{
			// 			cfroutes.CF_ROUTER:    &cfRouteMessage,
			// 			tcp_routes.TCP_ROUTER: &tcpRouteMessage,
			// 			routes.DIEGO_SSH:      &sshRouteMessage,
			// 		}))
			// 	})

			// 	Context("when generating the host key fails", func() {
			// 		BeforeEach(func() {
			// 			fakeKeyFactory.NewKeyPairReturns(nil, errors.New("boom"))
			// 		})

			// 		It("should return an error", func() {
			// 			Expect(err).To(HaveOccurred())
			// 		})
			// 	})

			// 	Context("when generating the user key fails", func() {
			// 		BeforeEach(func() {
			// 			errorCh := make(chan error, 2)
			// 			errorCh <- nil
			// 			errorCh <- errors.New("woops")

			// 			fakeKeyFactory.NewKeyPairStub = func(bits int) (keys.KeyPair, error) {
			// 				return nil, <-errorCh
			// 			}
			// 		})

			// 		It("should return an error", func() {
			// 			Expect(err).To(HaveOccurred())
			// 		})
			// 	})
			// })

			// Context("and it is setting the CPU weight", func() {
			// 	Context("when the memory limit is below the minimum value", func() {
			// 		BeforeEach(func() {
			// 			desiredAppReq.MemoryMB = recipebuilder.MinCpuProxy - 9999
			// 		})

			// 		It("returns 1", func() {
			// 			Expect(desiredLRP.CpuWeight).To(BeEquivalentTo(1))
			// 		})
			// 	})

			// 	Context("when the memory limit is above the maximum value", func() {
			// 		BeforeEach(func() {
			// 			desiredAppReq.MemoryMB = recipebuilder.MaxCpuProxy + 9999
			// 		})

			// 		It("returns 100", func() {
			// 			Expect(desiredLRP.CpuWeight).To(BeEquivalentTo(100))
			// 		})
			// 	})

			// 	Context("when the memory limit is in between the minimum and maximum value", func() {
			// 		BeforeEach(func() {
			// 			desiredAppReq.MemoryMB = (recipebuilder.MinCpuProxy + recipebuilder.MaxCpuProxy) / 2
			// 		})

			// 		It("returns 50", func() {
			// 			Expect(desiredLRP.CpuWeight).To(BeEquivalentTo(50))
			// 		})
			// 	})
			// })
		})

		Context("when there is a docker image url AND a droplet uri", func() {
			BeforeEach(func() {
				desiredAppReq.DockerImageUrl = "user/repo:tag"
				desiredAppReq.DropletUri = "http://the-droplet.uri.com"
			})

			It("should error", func() {
				Expect(err).To(MatchError(recipebuilder.ErrMultipleAppSources))
			})
		})

		Context("when there is no droplet uri", func() {
			BeforeEach(func() {
				desiredAppReq.DockerImageUrl = "user/repo:tag"
				desiredAppReq.DropletUri = ""
			})

			It("should error", func() {
				Expect(err).To(MatchError(recipebuilder.ErrDropletSourceMissing))
			})
		})

		Context("when there is no file descriptor limit", func() {
			BeforeEach(func() {
				desiredAppReq.FileDescriptors = 0
			})

			It("does not error", func() {
				Expect(err).NotTo(HaveOccurred())
			})

			It("sets a default FD limit on the run action", func() {
				Expect(replicationController.Spec.Template.Spec.Containers).To(HaveLen(1))

				appContainer := replicationController.Spec.Template.Spec.Containers[0]
				Expect(appContainer.Resources.Limits).To(HaveKeyWithValue(v1.ResourceName("cloudfoundry.org/file-descriptors"), resource.MustParse(fmt.Sprintf("%d", recipebuilder.DefaultFileDescriptorLimit))))
			})
		})

		Context("when requesting a stack that has not been configured", func() {
			BeforeEach(func() {
				desiredAppReq.Stack = "some-other-stack"
			})

			It("should error", func() {
				Expect(err).To(MatchError(recipebuilder.ErrNoLifecycleDefined))
			})
		})

		Context("when app ports are passed", func() {
			BeforeEach(func() {
				desiredAppReq.Ports = []uint32{1456, 2345, 3456}
			})

			It("builds a DesiredLRP with the correct ports", func() {
				Expect(replicationController.Spec.Template.Spec.Containers).To(HaveLen(1))
				Expect(replicationController.Spec.Template.Spec.Containers[0].Ports).To(ConsistOf(
					v1.ContainerPort{Protocol: v1.ProtocolTCP, ContainerPort: 1456},
					v1.ContainerPort{Protocol: v1.ProtocolTCP, ContainerPort: 2345},
					v1.ContainerPort{Protocol: v1.ProtocolTCP, ContainerPort: 3456},
				))
			})

			// 	It("sets the health check to the first provided port", func() {
			// 		Expect(desiredLRP.Monitor.GetValue()).To(Equal(models.Timeout(
			// 			&models.ParallelAction{
			// 				Actions: []*models.Action{
			// 					&models.Action{
			// 						RunAction: &models.RunAction{
			// 							User:      "vcap",
			// 							Path:      "/tmp/lifecycle/healthcheck",
			// 							Args:      []string{"-port=1456"},
			// 							LogSource: "HEALTH",
			// 							ResourceLimits: &models.ResourceLimits{
			// 								Nofile: &defaultNofile,
			// 							},
			// 							SuppressLogOutput: true,
			// 						},
			// 					},
			// 					&models.Action{
			// 						RunAction: &models.RunAction{
			// 							User:      "vcap",
			// 							Path:      "/tmp/lifecycle/healthcheck",
			// 							Args:      []string{"-port=2345"},
			// 							LogSource: "HEALTH",
			// 							ResourceLimits: &models.ResourceLimits{
			// 								Nofile: &defaultNofile,
			// 							},
			// 							SuppressLogOutput: true,
			// 						},
			// 					},
			// 					&models.Action{
			// 						RunAction: &models.RunAction{
			// 							User:      "vcap",
			// 							Path:      "/tmp/lifecycle/healthcheck",
			// 							Args:      []string{"-port=3456"},
			// 							LogSource: "HEALTH",
			// 							ResourceLimits: &models.ResourceLimits{
			// 								Nofile: &defaultNofile,
			// 							},
			// 							SuppressLogOutput: true,
			// 						},
			// 					},
			// 				},
			// 			},
			// 			30*time.Second,
			// 		)))
			// 	})
		})

		Context("when log source is empty", func() {
			BeforeEach(func() {
				desiredAppReq.LogSource = ""
			})

			It("uses APP", func() {
				Expect(replicationController.ObjectMeta.Annotations).To(HaveKeyWithValue("cloudfoundry.org/log-source", "APP"))
			})
		})

		// Describe("volume mounts", func() {
		// 	Context("when none are provided", func() {
		// 		It("is empty", func() {
		// 			Expect(len(desiredLRP.VolumeMounts)).To(Equal(0))
		// 		})
		// 	})

		// 	Context("when some are provided", func() {
		// 		var testVolume models.VolumeMount

		// 		BeforeEach(func() {
		// 			testVolume = models.VolumeMount{
		// 				Driver:        "testdriver",
		// 				VolumeId:      "volumeId",
		// 				ContainerPath: "/Volumes/myvol",
		// 				Mode:          models.BindMountMode_RW,
		// 				Config:        []byte("config stuff"),
		// 			}
		// 			desiredAppReq.VolumeMounts = []*models.VolumeMount{&testVolume}
		// 		})

		// 		It("desires the mounts", func() {
		// 			Expect(desiredLRP.VolumeMounts).To(Equal([]*models.VolumeMount{&testVolume}))
		// 		})
		// 	})
		// })
	})

	Describe("ExtractExposedPorts", func() {
		Context("when desired app request has ports specified", func() {
			BeforeEach(func() {
				desiredAppReq.Ports = []uint32{1456, 2345, 3456}
			})

			It("returns the ports specified in desired app request as exposed ports", func() {
				ports, err := builder.ExtractExposedPorts(&desiredAppReq)
				Expect(err).ToNot(HaveOccurred())
				Expect(ports).To(Equal(desiredAppReq.Ports))
			})
		})

		Context("when desired app request does not have any ports specified", func() {
			It("returns the slice with default port", func() {
				ports, err := builder.ExtractExposedPorts(&desiredAppReq)
				Expect(err).ToNot(HaveOccurred())
				Expect(ports).To(Equal([]uint32{8080}))
			})
		})
	})

	Describe("BuildTask", func() {
		var (
			err            error
			newTaskReq     cc_messages.TaskRequestFromCC
			taskDefinition *models.TaskDefinition
		)

		BeforeEach(func() {
			newTaskReq = cc_messages.TaskRequestFromCC{
				LogGuid:   "some-log-guid",
				MemoryMb:  128,
				DiskMb:    512,
				Lifecycle: "docker",
				EnvironmentVariables: []*models.EnvironmentVariable{
					{Name: "foo", Value: "bar"},
					{Name: "VCAP_APPLICATION", Value: "{\"application_name\":\"my-app\"}"},
				},
				DropletUri:            "http://the-droplet.uri.com",
				DropletHash:           "some-hash",
				RootFs:                "some-stack",
				CompletionCallbackUrl: "http://api.cc.com/v1/tasks/complete",
				Command:               "the-start-command",
				EgressRules:           egressRules,
				LogSource:             "APP/TASK/my-task",
			}
		})

		JustBeforeEach(func() {
			taskDefinition, err = builder.BuildTask(&newTaskReq)
		})

		Describe("when no droplet hash is set", func() {
			BeforeEach(func() {
				newTaskReq.DropletHash = ""
			})

			It("does not populate ChecksumAlgorithm and ChecksumValue", func() {
				expectedAction := models.Serial(&models.DownloadAction{
					From:     newTaskReq.DropletUri,
					To:       ".",
					CacheKey: "",
					User:     "vcap",
				},
					&models.RunAction{
						User:           "vcap",
						Path:           "/tmp/lifecycle/launcher",
						Args:           []string{"app", "the-start-command", ""},
						Env:            newTaskReq.EnvironmentVariables,
						LogSource:      "APP/TASK/my-task",
						ResourceLimits: &models.ResourceLimits{},
					},
				)
				Expect(taskDefinition.Action.GetValue()).To(Equal(expectedAction))
			})
		})

		Describe("CPU weight calculation", func() {
			Context("when the memory limit is below the minimum value", func() {
				BeforeEach(func() {
					newTaskReq.MemoryMb = recipebuilder.MinCpuProxy - 9999
				})

				It("returns 1", func() {
					Expect(taskDefinition.CpuWeight).To(BeEquivalentTo(1))
				})
			})

			Context("when the memory limit is above the maximum value", func() {
				BeforeEach(func() {
					newTaskReq.MemoryMb = recipebuilder.MaxCpuProxy + 9999
				})

				It("returns 100", func() {
					Expect(taskDefinition.CpuWeight).To(BeEquivalentTo(100))
				})
			})

			Context("when the memory limit is in between the minimum and maximum value", func() {
				BeforeEach(func() {
					newTaskReq.MemoryMb = (recipebuilder.MinCpuProxy + recipebuilder.MaxCpuProxy) / 2
				})

				It("returns 50", func() {
					Expect(taskDefinition.CpuWeight).To(BeEquivalentTo(50))
				})
			})
		})

		It("returns the desired TaskDefinition", func() {
			Expect(err).NotTo(HaveOccurred())
			Expect(taskDefinition.LogGuid).To(Equal("some-log-guid"))
			Expect(taskDefinition.MemoryMb).To(BeEquivalentTo(128))
			Expect(taskDefinition.DiskMb).To(BeEquivalentTo(512))
			Expect(taskDefinition.EnvironmentVariables).To(Equal(newTaskReq.EnvironmentVariables))
			Expect(taskDefinition.RootFs).To(Equal(models.PreloadedRootFS("some-stack")))
			Expect(taskDefinition.CompletionCallbackUrl).To(Equal("http://api.cc.com/v1/tasks/complete"))
			Expect(taskDefinition.Privileged).To(BeFalse())
			Expect(taskDefinition.EgressRules).To(ConsistOf(egressRules))
			Expect(taskDefinition.TrustedSystemCertificatesPath).To(Equal(recipebuilder.TrustedSystemCertificatesPath))
			Expect(taskDefinition.LogSource).To(Equal("APP/TASK/my-task"))

			expectedAction := models.Serial(&models.DownloadAction{
				From:              newTaskReq.DropletUri,
				To:                ".",
				CacheKey:          "",
				User:              "vcap",
				ChecksumAlgorithm: "sha1",
				ChecksumValue:     "some-hash",
			},
				&models.RunAction{
					User:           "vcap",
					Path:           "/tmp/lifecycle/launcher",
					Args:           []string{"app", "the-start-command", ""},
					Env:            newTaskReq.EnvironmentVariables,
					LogSource:      "APP/TASK/my-task",
					ResourceLimits: &models.ResourceLimits{},
				},
			)
			Expect(taskDefinition.Action.GetValue()).To(Equal(expectedAction))

			expectedCacheDependencies := []*models.CachedDependency{
				&models.CachedDependency{
					From:     "http://file-server.com/v1/static/some-lifecycle.tgz",
					To:       "/tmp/lifecycle",
					CacheKey: "buildpack-some-stack-lifecycle",
				},
			}

			Expect(taskDefinition.LegacyDownloadUser).To(Equal("vcap"))
			Expect(taskDefinition.CachedDependencies).To(BeEquivalentTo(expectedCacheDependencies))
		})

		Context("when the droplet uri is missing", func() {
			BeforeEach(func() {
				newTaskReq.DropletUri = ""
			})

			It("returns an error", func() {
				Expect(err).To(Equal(recipebuilder.ErrDropletSourceMissing))
			})
		})

		Context("when the docker path is specified", func() {
			BeforeEach(func() {
				newTaskReq.DockerPath = "jim/jim"
			})

			It("returns an error", func() {
				Expect(err).To(Equal(recipebuilder.ErrMultipleAppSources))
			})
		})

		Context("when the lifecycle does not exist", func() {
			BeforeEach(func() {
				newTaskReq.RootFs = "some-other-rootfs"
			})

			It("returns an error", func() {
				Expect(err).To(Equal(recipebuilder.ErrNoLifecycleDefined))
			})
		})

		Describe("volume mounts", func() {
			Context("when none are provided", func() {
				It("is empty", func() {
					Expect(len(taskDefinition.VolumeMounts)).To(Equal(0))
				})
			})

			Context("when some are provided", func() {
				var testVolume models.VolumeMount

				BeforeEach(func() {
					testVolume = models.VolumeMount{
						Driver:        "testdriver",
						VolumeId:      "volumeId",
						ContainerPath: "/Volumes/myvol",
						Mode:          models.BindMountMode_RW,
						Config:        []byte("config stuff"),
					}
					newTaskReq.VolumeMounts = []*models.VolumeMount{&testVolume}
				})

				It("desires the mounts", func() {
					Expect(taskDefinition.VolumeMounts).To(Equal([]*models.VolumeMount{&testVolume}))
				})
			})
		})
	})
})

func MustEncode(value interface{}) string {
	encoded, err := json.Marshal(value)
	Expect(err).NotTo(HaveOccurred())

	return string(encoded)
}
