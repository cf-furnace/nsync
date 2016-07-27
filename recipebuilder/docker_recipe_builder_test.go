package recipebuilder_test

import (
	"encoding/json"

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
	"github.com/onsi/gomega/gbytes"
	"github.com/pivotal-golang/lager/lagertest"
)

var _ = Describe("Docker Recipe Builder", func() {
	var (
		builder        *recipebuilder.DockerRecipeBuilder
		lifecycles     map[string]string
		egressRules    []*models.SecurityGroupRule
		networkInfo    *models.Network
		fakeKeyFactory *fake_keys.FakeSSHKeyFactory
		logger         *lagertest.TestLogger
		err            error
		desiredAppReq  cc_messages.DesireAppRequestFromCC
		expectedRoutes models.Routes
		processGuid    helpers.ProcessGuid
	)

	//defaultNofile := recipebuilder.DefaultFileDescriptorLimit

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

		routingInfo, err := cc_messages.CCHTTPRoutes{
			{Hostname: "route1"},
			{Hostname: "route2"},
		}.CCRouteInfo()
		Expect(err).NotTo(HaveOccurred())

		processGuid, err = helpers.NewProcessGuid("8d58c09b-b305-4f16-bcfe-b78edcb77100-3f258eb0-9dac-460c-a424-b43fe92bee27")
		Expect(err).NotTo(HaveOccurred())

		desiredAppReq = cc_messages.DesireAppRequestFromCC{
			ProcessGuid:       processGuid.String(),
			Stack:             "some-stack",
			StartCommand:      "the-start-command with-arguments",
			DockerImageUrl:    "user/repo:tag",
			ExecutionMetadata: "{}",
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

		builder = recipebuilder.NewDockerRecipeBuilder(logger, config)
	})

	Context("Build", func() {
		var replicationController *v1.ReplicationController

		JustBeforeEach(func() {
			replicationController, err = builder.BuildReplicationController(&desiredAppReq)
		})

		Context("when ports is an empty array", func() {
			BeforeEach(func() {
				desiredAppReq.Ports = []uint32{}
			})

			It("requests only default ports on the RC", func() {
				Expect(replicationController.Spec.Template.Spec.Containers[0].Ports).To(HaveLen(1))
				Expect(replicationController.Spec.Template.Spec.Containers[0].Ports[0]).To(Equal(v1.ContainerPort{Protocol: v1.ProtocolTCP, ContainerPort: 8080}))
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

			It("builds a valid Desired RC", func() {
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

				Expect(replicationController.Spec.Template.ObjectMeta.Labels).To(HaveKeyWithValue("cloudfoundry.org/app-guid", processGuid.AppGuid.String()))
				Expect(replicationController.Spec.Template.ObjectMeta.Labels).To(HaveKeyWithValue("cloudfoundry.org/space-guid", "space-guid"))
				Expect(replicationController.Spec.Template.ObjectMeta.Labels).To(HaveKeyWithValue("cloudfoundry.org/process-guid", processGuid.ShortenedGuid()))

				Expect(replicationController.Spec.Template.Spec.Containers).To(HaveLen(1))
				appContainer := replicationController.Spec.Template.Spec.Containers[0]

				Expect(appContainer.Name).To(Equal("application"))
				Expect(appContainer.Image).To(Equal(desiredAppReq.DockerImageUrl))
				Expect(appContainer.ImagePullPolicy).To(BeEquivalentTo("IfNotPresent"))

				Expect(appContainer.Resources.Limits).To(HaveKeyWithValue(v1.ResourceCPU, resource.MustParse("1000m")))
				Expect(appContainer.Resources.Limits).To(HaveKeyWithValue(v1.ResourceMemory, resource.MustParse("128Mi")))
				Expect(appContainer.Resources.Limits).To(HaveKeyWithValue(v1.ResourceName("cloudfoundry.org/storage-space"), resource.MustParse("512Mi")))
				Expect(appContainer.Resources.Limits).To(HaveKeyWithValue(v1.ResourceName("cloudfoundry.org/file-descriptors"), resource.MustParse("32")))

				Expect(appContainer.Ports).To(ConsistOf(v1.ContainerPort{Protocol: v1.ProtocolTCP, ContainerPort: 8080}))

			})

			/*Context("when no health check is specified", func() {
					BeforeEach(func() {
						desiredAppReq.HealthCheckType = cc_messages.UnspecifiedHealthCheckType
					})

					It("sets up the port check for backwards compatibility", func() {
						downloadDestinations := []string{}
						for _, dep := range desiredLRP.CachedDependencies {
							if dep != nil {
								downloadDestinations = append(downloadDestinations, dep.To)
							}
						}

						Expect(downloadDestinations).To(ContainElement("/tmp/lifecycle"))

						Expect(desiredLRP.Monitor.GetValue()).To(Equal(models.Timeout(
							&models.ParallelAction{
								Actions: []*models.Action{
									&models.Action{
										RunAction: &models.RunAction{
											User:      "root",
											Path:      "/tmp/lifecycle/healthcheck",
											Args:      []string{"-port=8080"},
											LogSource: "HEALTH",
											ResourceLimits: &models.ResourceLimits{
												Nofile: &defaultNofile,
											},
											SuppressLogOutput: true,
										},
									},
								},
							},
							30*time.Second,
						)))
					})
				})

				Context("when the 'none' health check is specified", func() {
					BeforeEach(func() {
						desiredAppReq.HealthCheckType = cc_messages.NoneHealthCheckType
					})

					It("does not populate the monitor action", func() {
						Expect(desiredLRP.Monitor).To(BeNil())
					})

					It("still downloads the lifecycle, since we need it for the launcher", func() {
						downloadDestinations := []string{}
						for _, dep := range desiredLRP.CachedDependencies {
							if dep != nil {
								downloadDestinations = append(downloadDestinations, dep.To)
							}
						}

						Expect(downloadDestinations).To(ContainElement("/tmp/lifecycle"))
					})
				})

				Context("when allow ssh is true", func() {
					BeforeEach(func() {
						desiredAppReq.AllowSSH = true

						keyPairChan := make(chan keys.KeyPair, 2)

						fakeHostKeyPair := &fake_keys.FakeKeyPair{}
						fakeHostKeyPair.PEMEncodedPrivateKeyReturns("pem-host-private-key")
						fakeHostKeyPair.FingerprintReturns("host-fingerprint")

						fakeUserKeyPair := &fake_keys.FakeKeyPair{}
						fakeUserKeyPair.AuthorizedKeyReturns("authorized-user-key")
						fakeUserKeyPair.PEMEncodedPrivateKeyReturns("pem-user-private-key")

						keyPairChan <- fakeHostKeyPair
						keyPairChan <- fakeUserKeyPair

						fakeKeyFactory.NewKeyPairStub = func(bits int) (keys.KeyPair, error) {
							return <-keyPairChan, nil
						}
					})

					It("setup should download the ssh daemon", func() {
						expectedCacheDependencies := []*models.CachedDependency{
							{
								From:     "http://file-server.com/v1/static/the/docker/lifecycle/path.tgz",
								To:       "/tmp/lifecycle",
								CacheKey: "docker-lifecycle",
							},
						}

						Expect(desiredLRP.CachedDependencies).To(BeEquivalentTo(expectedCacheDependencies))
					})

					It("runs the ssh daemon in the container", func() {
						expectedNumFiles := uint64(32)

						expectedAction := models.Codependent(
							&models.RunAction{
								User: "root",
								Path: "/tmp/lifecycle/launcher",
								Args: []string{
									"app",
									"the-start-command with-arguments",
									"{}",
								},
								Env: []*models.EnvironmentVariable{
									{Name: "foo", Value: "bar"},
									{Name: "PORT", Value: "8080"},
								},
								ResourceLimits: &models.ResourceLimits{
									Nofile: &expectedNumFiles,
								},
								LogSource: "MYSOURCE",
							},
							&models.RunAction{
								User: "root",
								Path: "/tmp/lifecycle/diego-sshd",
								Args: []string{
									"-address=0.0.0.0:2222",
									"-hostKey=pem-host-private-key",
									"-authorizedKey=authorized-user-key",
									"-inheritDaemonEnv",
									"-logLevel=fatal",
								},
								Env: []*models.EnvironmentVariable{
									{Name: "foo", Value: "bar"},
									{Name: "PORT", Value: "8080"},
								},
								ResourceLimits: &models.ResourceLimits{
									Nofile: &expectedNumFiles,
								},
							},
						)

						Expect(desiredLRP.Action.GetValue()).To(Equal(expectedAction))
					})

					It("opens up the default ssh port", func() {
						Expect(desiredLRP.Ports).To(Equal([]uint32{
							8080,
							2222,
						}))
					})

					It("declares ssh routing information in the LRP", func() {
						cfRoutePayload, err := json.Marshal(cfroutes.CFRoutes{
							{Hostnames: []string{"route1", "route2"}, Port: 8080},
						})
						Expect(err).NotTo(HaveOccurred())

						sshRoutePayload, err := json.Marshal(routes.SSHRoute{
							ContainerPort:   2222,
							PrivateKey:      "pem-user-private-key",
							HostFingerprint: "host-fingerprint",
						})
						Expect(err).NotTo(HaveOccurred())

						cfRouteMessage := json.RawMessage(cfRoutePayload)
						tcpRouteMessage := json.RawMessage([]byte("[]"))
						sshRouteMessage := json.RawMessage(sshRoutePayload)

						Expect(desiredLRP.Routes).To(Equal(&models.Routes{
							cfroutes.CF_ROUTER:    &cfRouteMessage,
							tcp_routes.TCP_ROUTER: &tcpRouteMessage,
							routes.DIEGO_SSH:      &sshRouteMessage,
						}))
					})

					Context("when generating the host key fails", func() {
						BeforeEach(func() {
							fakeKeyFactory.NewKeyPairReturns(nil, errors.New("boom"))
						})

						It("should return an error", func() {
							Expect(err).To(HaveOccurred())
						})
					})

					Context("when generating the user key fails", func() {
						BeforeEach(func() {
							errorCh := make(chan error, 2)
							errorCh <- nil
							errorCh <- errors.New("woops")

							fakeKeyFactory.NewKeyPairStub = func(bits int) (keys.KeyPair, error) {
								return nil, <-errorCh
							}
						})

						It("should return an error", func() {
							Expect(err).To(HaveOccurred())
						})
					})
				})
			})*/

			Context("when there is a docker image url instead of a droplet uri", func() {
				BeforeEach(func() {
					desiredAppReq.DockerImageUrl = "user/repo:tag"
					desiredAppReq.ExecutionMetadata = "{}"
				})

				It("does not error", func() {
					Expect(err).NotTo(HaveOccurred())
				})

				Context("when ports are passed in desired app request", func() {
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
				})

				Context("when the docker image exposes several ports in its metadata", func() {
					BeforeEach(func() {
						desiredAppReq.ExecutionMetadata = `{"ports":[
					{"Port":320, "Protocol": "udp"},
					{"Port":8081, "Protocol": "tcp"},
					{"Port":8082, "Protocol": "tcp"}
					]}`
						routingInfo, err := cc_messages.CCHTTPRoutes{
							{Hostname: "route1", Port: 8081},
							{Hostname: "route2", Port: 8082},
						}.CCRouteInfo()
						Expect(err).NotTo(HaveOccurred())
						desiredAppReq.RoutingInfo = routingInfo
					})

					It("exposes all encountered tcp ports", func() {
						Expect(replicationController.Spec.Template.Spec.Containers).To(HaveLen(1))
						Expect(replicationController.Spec.Template.Spec.Containers[0].Ports).To(ConsistOf(
							v1.ContainerPort{Protocol: v1.ProtocolTCP, ContainerPort: 8081},
							v1.ContainerPort{Protocol: v1.ProtocolTCP, ContainerPort: 8082},
						))

					})

					Context("when ports in desired app request is empty slice", func() {
						BeforeEach(func() {
							desiredAppReq.Ports = []uint32{}
						})
						It("exposes all encountered tcp ports", func() {
							Expect(replicationController.Spec.Template.Spec.Containers).To(HaveLen(1))
							Expect(replicationController.Spec.Template.Spec.Containers[0].Ports).To(ConsistOf(
								v1.ContainerPort{Protocol: v1.ProtocolTCP, ContainerPort: 8081},
								v1.ContainerPort{Protocol: v1.ProtocolTCP, ContainerPort: 8082},
							))
						})
					})
				})

				Context("when the docker image exposes several non-tcp ports in its metadata", func() {
					BeforeEach(func() {
						desiredAppReq.ExecutionMetadata = `{"ports":[
					{"Port":319, "Protocol": "udp"},
					{"Port":320, "Protocol": "udp"}
					]}`
					})

					It("errors", func() {
						Expect(err).To(HaveOccurred())
						Expect(logger.TestSink.Buffer()).To(gbytes.Say("parsing-exposed-ports-failed"))
						Expect(logger.TestSink.Buffer()).To(gbytes.Say("No tcp ports found in image metadata"))
					})
				})

				Context("when the docker image execution metadata is not valid json", func() {
					BeforeEach(func() {
						desiredAppReq.ExecutionMetadata = "invalid-json"
					})

					It("errors", func() {
						Expect(err).To(HaveOccurred())
						Expect(logger.TestSink.Buffer()).To(gbytes.Say("parsing-execution-metadata-failed"))
					})
				})

				/*testHealthcheckActionUser := func(user string) func() {
					return func() {
						timeoutAction := desiredLRP.Monitor.TimeoutAction

						healthcheckRunAction := timeoutAction.Action.ParallelAction.Actions[0].RunAction
						Expect(healthcheckRunAction.User).To(Equal(user))
					}
				}*/

				Context("and the docker image url has scheme", func() {
					BeforeEach(func() {
						desiredAppReq.DockerImageUrl = "https://docker.io/repo"
					})

					It("errors", func() {
						Expect(err).To(HaveOccurred())
					})
				})
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

			Context("when there is NEITHER a docker image url NOR a droplet uri", func() {
				BeforeEach(func() {
					desiredAppReq.DockerImageUrl = ""
					desiredAppReq.DropletUri = ""
				})

				It("should error", func() {
					Expect(err).To(MatchError(recipebuilder.ErrDockerImageMissing))
				})
			})

			Context("when there is no file descriptor limit", func() {
				BeforeEach(func() {
					desiredAppReq.FileDescriptors = 0
				})

				It("does not error", func() {
					Expect(err).NotTo(HaveOccurred())
				})
			})

		})
	})

	/*Context("When the recipeBuilder Config has Privileged set to true", func() {
		BeforeEach(func() {
			config := recipebuilder.Config{
				Lifecycles:           lifecycles,
				FileServerURL:        "http://file-server.com",
				KeyFactory:           fakeKeyFactory,
				PrivilegedContainers: true,
			}
			builder = recipebuilder.NewDockerRecipeBuilder(logger, config)
		})

		It("sets Priviledged to false", func() {
			Expect(desiredLRP.Privileged).To(BeFalse())
		})

	})*/

	/*Describe("volume mounts", func() {
			Context("when none are provided", func() {
				It("is empty", func() {
					Expect(len(desiredLRP.VolumeMounts)).To(Equal(0))
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
					desiredAppReq.VolumeMounts = []*models.VolumeMount{&testVolume}
				})

				It("desires the mounts", func() {
					Expect(desiredLRP.VolumeMounts).To(Equal([]*models.VolumeMount{&testVolume}))
				})
			})
		})

	})*/

	/*Context("BuildTask", func() {
		var (
			newTaskReq     *cc_messages.TaskRequestFromCC
			taskDefinition *models.TaskDefinition
			err            error
		)

		BeforeEach(func() {
			newTaskReq = &cc_messages.TaskRequestFromCC{
				LogGuid:     "the-log-guid",
				DiskMb:      128,
				MemoryMb:    512,
				EgressRules: egressRules,
				EnvironmentVariables: []*models.EnvironmentVariable{
					{Name: "foo", Value: "bar"},
				},
				CompletionCallbackUrl: "http://google.com",
				Command:               "docker run fast",
				DockerPath:            "cloudfoundry/diego-docker-app",
				LogSource:             "APP/TASK/my-task",
			}
		})

		JustBeforeEach(func() {
			taskDefinition, err = builder.BuildTask(newTaskReq)
		})

		It("does not error", func() {
			Expect(err).NotTo(HaveOccurred())
		})

		It("builds a task definition", func() {
			Expect(taskDefinition.DiskMb).To(BeEquivalentTo(128))
			Expect(taskDefinition.MemoryMb).To(BeEquivalentTo(512))
			Expect(taskDefinition.LogGuid).To(Equal("the-log-guid"))
			Expect(taskDefinition.Privileged).To(BeFalse())

			Expect(taskDefinition.EgressRules).To(Equal(egressRules))
			Expect(taskDefinition.EnvironmentVariables).To(Equal([]*models.EnvironmentVariable{
				{Name: "foo", Value: "bar"},
			}))
			Expect(taskDefinition.CompletionCallbackUrl).To(Equal("http://google.com"))
			Expect(taskDefinition.RootFs).To(Equal("docker:///cloudfoundry/diego-docker-app"))
			Expect(taskDefinition.LogSource).To(Equal("APP/TASK/my-task"))

			expectedCacheDependencies := []*models.CachedDependency{
				&models.CachedDependency{
					From:     "http://file-server.com/v1/static/the/docker/lifecycle/path.tgz",
					To:       "/tmp/lifecycle",
					CacheKey: "docker-lifecycle",
				},
			}

			Expect(taskDefinition.LegacyDownloadUser).To(Equal("vcap"))
			Expect(taskDefinition.CachedDependencies).To(BeEquivalentTo(expectedCacheDependencies))

			expectedAction := models.WrapAction(&models.RunAction{
				User: "root",
				Path: "/tmp/lifecycle/launcher",
				Args: append(
					[]string{"app"},
					"docker run fast",
					"{}",
				),
				Env:            newTaskReq.EnvironmentVariables,
				ResourceLimits: &models.ResourceLimits{},
				LogSource:      "APP/TASK/my-task",
			})

			Expect(taskDefinition.Action).To(BeEquivalentTo(expectedAction))

			Expect(taskDefinition.TrustedSystemCertificatesPath).To(Equal(recipebuilder.TrustedSystemCertificatesPath))
		})

		Context("When the recipeBuilder Config has Privileged set to true", func() {
			BeforeEach(func() {
				config := recipebuilder.Config{
					Lifecycles:           lifecycles,
					FileServerURL:        "http://file-server.com",
					KeyFactory:           fakeKeyFactory,
					PrivilegedContainers: true,
				}
				builder = recipebuilder.NewDockerRecipeBuilder(logger, config)
			})

			It("sets Priviledged to false", func() {
				Expect(taskDefinition.Privileged).To(BeFalse())
			})

		})
		Context("when the docker path is not specified", func() {
			BeforeEach(func() {
				newTaskReq.DockerPath = ""
			})

			It("returns an error", func() {
				Expect(err).To(Equal(recipebuilder.ErrDockerImageMissing))
			})
		})

		Context("with an invalid docker path url", func() {
			BeforeEach(func() {
				newTaskReq.DockerPath = "docker://jim/jim"
			})

			It("returns an error", func() {
				Expect(err).To(HaveOccurred())
			})
		})

		Context("when a droplet uri is specified", func() {
			BeforeEach(func() {
				newTaskReq.DropletUri = "https://utako.utako.com"
			})

			It("returns an error", func() {
				Expect(err).To(Equal(recipebuilder.ErrMultipleAppSources))
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
	})*/
})
