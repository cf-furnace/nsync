package handlers_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"net/url"

	"k8s.io/kubernetes/pkg/api"

	"github.com/cf-furnace/nsync/bulk/fakes"
	"github.com/cf-furnace/nsync/handlers"
	"github.com/cf-furnace/nsync/handlers/transformer"
	"github.com/cf-furnace/nsync/handlers/unversionedfakes"
	"github.com/cf-furnace/nsync/recipebuilder"
	"github.com/cloudfoundry-incubator/bbs/fake_bbs"
	"github.com/cloudfoundry-incubator/bbs/models"
	"github.com/cloudfoundry-incubator/runtime-schema/cc_messages"
	"github.com/cloudfoundry/dropsonde/metric_sender/fake"
	"github.com/cloudfoundry/dropsonde/metrics"
	"github.com/pivotal-golang/lager/lagertest"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
)

var _ = Describe("DesireAppHandler", func() {
	var (
		logger           *lagertest.TestLogger
		fakeBBS          *fake_bbs.FakeClient
		fakeK8s          *unversionedfakes.FakeInterface
		buildpackBuilder *fakes.FakeRecipeBuilder
		dockerBuilder    *fakes.FakeRecipeBuilder
		desireAppRequest cc_messages.DesireAppRequestFromCC
		metricSender     *fake.FakeMetricSender

		request           *http.Request
		responseRecorder  *httptest.ResponseRecorder
		expectedNamespace string
	)

	BeforeEach(func() {
		var err error

		logger = lagertest.NewTestLogger("test")
		fakeBBS = &fake_bbs.FakeClient{}
		fakeK8s = &unversionedfakes.FakeInterface{}
		buildpackBuilder = new(fakes.FakeRecipeBuilder)
		dockerBuilder = new(fakes.FakeRecipeBuilder)
		expectedNamespace = "linsun"

		routingInfo, err := cc_messages.CCHTTPRoutes{
			{Hostname: "route1"},
			{Hostname: "route2"},
		}.CCRouteInfo()
		Expect(err).NotTo(HaveOccurred())

		desireAppRequest = cc_messages.DesireAppRequestFromCC{
			ProcessGuid:  "some-guid",
			DropletUri:   "http://the-droplet.uri.com",
			Stack:        "some-stack",
			StartCommand: "the-start-command",
			Environment: []*models.EnvironmentVariable{
				{Name: "foo", Value: "bar"},
				{Name: "VCAP_APPLICATION", Value: "{\"application_name\":\"my-app\"}"},
			},
			MemoryMB:        128,
			DiskMB:          512,
			FileDescriptors: 32,
			NumInstances:    2,
			RoutingInfo:     routingInfo,
			LogGuid:         "some-log-guid",
			ETag:            "last-modified-etag",
		}

		metricSender = fake.NewFakeMetricSender()
		metrics.Initialize(metricSender, nil)

		responseRecorder = httptest.NewRecorder()

		request, err = http.NewRequest("POST", "", nil)
		Expect(err).NotTo(HaveOccurred())
		request.Form = url.Values{
			":process_guid": []string{"some-guid"},
		}
	})

	JustBeforeEach(func() {
		if request.Body == nil {
			jsonBytes, err := json.Marshal(&desireAppRequest)
			Expect(err).NotTo(HaveOccurred())
			reader := bytes.NewReader(jsonBytes)

			request.Body = ioutil.NopCloser(reader)
		}

		handler := handlers.NewDesireAppHandler(logger, map[string]recipebuilder.RecipeBuilder{
			"buildpack": buildpackBuilder,
			"docker":    dockerBuilder,
		}, fakeK8s)
		handler.DesireApp(responseRecorder, request)
	})

	Context("when the desired LRP does not exist", func() {
		var fakeNamespace *unversionedfakes.FakeNamespaceInterface
		var fakeReplicationController *unversionedfakes.FakeReplicationControllerInterface

		Context("when the namespace is missing", func() {
			BeforeEach(func() {
				fakeNamespace = &unversionedfakes.FakeNamespaceInterface{}
				fakeReplicationController = &unversionedfakes.FakeReplicationControllerInterface{}

				fakeK8s.NamespacesReturns(fakeNamespace)
				fakeNamespace.GetReturns(nil, errors.New("namespace doesn't exist yet"))
				fakeK8s.ReplicationControllersReturns(fakeReplicationController)

			})

			It("creates the namespace", func() {
				Expect(fakeK8s.NamespacesCallCount()).To(Equal(2))
				Expect(fakeNamespace.GetCallCount()).To(Equal(1))
				Expect(fakeNamespace.GetArgsForCall(0)).To(Equal(expectedNamespace))

				Expect(fakeNamespace.CreateCallCount()).To(Equal(1))
				Expect(fakeNamespace.CreateArgsForCall(0).ObjectMeta.Name).To(Equal(expectedNamespace))

			})
		})

		Context("when the namespace already exists", func() {
			var apiNS *api.Namespace

			BeforeEach(func() {
				fakeNamespace = &unversionedfakes.FakeNamespaceInterface{}
				fakeReplicationController = &unversionedfakes.FakeReplicationControllerInterface{}

				apiNS = &api.Namespace{
					ObjectMeta: api.ObjectMeta{
						Name: expectedNamespace,
					},
					Spec: api.NamespaceSpec{
						Finalizers: []api.FinalizerName{api.FinalizerName(expectedNamespace)},
					},
				}

				fakeK8s.NamespacesReturns(fakeNamespace)
				fakeNamespace.GetReturns(apiNS, nil)
				fakeK8s.ReplicationControllersReturns(fakeReplicationController)
			})

			It("creates the desired LRP - replication controllers", func() {
				Expect(fakeK8s.ReplicationControllersCallCount()).To(Equal(2))
				Expect(fakeK8s.ReplicationControllersArgsForCall(0)).To(Equal(expectedNamespace))
				Expect(fakeK8s.ReplicationControllersArgsForCall(1)).To(Equal(expectedNamespace))
				Expect(fakeReplicationController.GetCallCount()).To(Equal(1))
				Expect(fakeReplicationController.CreateCallCount()).To(Equal(1))
				actualRC := fakeReplicationController.CreateArgsForCall(0)
				expectedRC, err := transformer.DesiredAppToRC(logger, desireAppRequest)
				Expect(err).NotTo(HaveOccurred())
				Expect(actualRC).To(Equal(expectedRC))
			})

			It("logs the incoming and outgoing request", func() {
				Eventually(logger.TestSink.Buffer).Should(gbytes.Say("request-from-cc"))
				Eventually(logger.TestSink.Buffer).Should(gbytes.Say("creating-desired-lrp"))
			})

			It("responds with 202 Accepted", func() {
				Expect(responseRecorder.Code).To(Equal(http.StatusAccepted))
			})

			It("increments the desired LRPs counter", func() {
				Expect(metricSender.GetCounter("LRPsDesired")).To(Equal(uint64(1)))
			})
		})

		Context("when the kubernetes fails", func() {
			BeforeEach(func() {
				fakeNamespace = &unversionedfakes.FakeNamespaceInterface{}
				fakeReplicationController = &unversionedfakes.FakeReplicationControllerInterface{}
				fakeK8s.NamespacesReturns(fakeNamespace)
				fakeK8s.ReplicationControllersReturns(fakeReplicationController)
				fakeReplicationController.CreateReturns(nil, errors.New("oh no"))
			})

			It("responds with a ServiceUnavailabe error", func() {
				Expect(responseRecorder.Code).To(Equal(http.StatusServiceUnavailable))
			})
		})

		Context("when the LRP has docker image", func() {
			var apiNS *api.Namespace

			BeforeEach(func() {
				desireAppRequest.DropletUri = ""
				desireAppRequest.DockerImageUrl = "docker:///user/repo#tag"

				fakeNamespace = &unversionedfakes.FakeNamespaceInterface{}
				fakeReplicationController = &unversionedfakes.FakeReplicationControllerInterface{}

				apiNS = &api.Namespace{
					ObjectMeta: api.ObjectMeta{
						Name: expectedNamespace,
					},
					Spec: api.NamespaceSpec{
						Finalizers: []api.FinalizerName{api.FinalizerName(expectedNamespace)},
					},
				}

				fakeK8s.NamespacesReturns(fakeNamespace)
				fakeNamespace.GetReturns(apiNS, nil)
				fakeK8s.ReplicationControllersReturns(fakeReplicationController)
			})

			It("creates the desired LRP in kubernetes", func() {
				Expect(fakeK8s.ReplicationControllersCallCount()).To(Equal(2))
				Expect(fakeK8s.ReplicationControllersArgsForCall(0)).To(Equal(expectedNamespace))
				Expect(fakeK8s.ReplicationControllersArgsForCall(1)).To(Equal(expectedNamespace))
				Expect(fakeReplicationController.GetCallCount()).To(Equal(1))
				Expect(fakeReplicationController.CreateCallCount()).To(Equal(1))
				actualRC := fakeReplicationController.CreateArgsForCall(0)
				expectedRC, err := transformer.DesiredAppToRC(logger, desireAppRequest)
				Expect(err).NotTo(HaveOccurred())
				Expect(actualRC).To(Equal(expectedRC))
			})

			It("responds with 202 Accepted", func() {
				Expect(responseRecorder.Code).To(Equal(http.StatusAccepted))
			})

			It("increments the desired LRPs counter", func() {
				Expect(metricSender.GetCounter("LRPsDesired")).To(Equal(uint64(1)))
			})
		})
	})

	Context("when desired LRP already exists", func() {
		var fakeNamespace *unversionedfakes.FakeNamespaceInterface
		var fakeReplicationController *unversionedfakes.FakeReplicationControllerInterface

		BeforeEach(func() {
			fakeNamespace = &unversionedfakes.FakeNamespaceInterface{}
			fakeReplicationController = &unversionedfakes.FakeReplicationControllerInterface{}
			apiNS := &api.Namespace{
				ObjectMeta: api.ObjectMeta{
					Name: expectedNamespace,
				},
				Spec: api.NamespaceSpec{
					Finalizers: []api.FinalizerName{api.FinalizerName(expectedNamespace)},
				},
			}
			fakeK8s.NamespacesReturns(fakeNamespace)
			fakeNamespace.GetReturns(apiNS, nil)

			fakeK8s.ReplicationControllersReturns(fakeReplicationController)
			existingRC, err := transformer.DesiredAppToRC(logger, desireAppRequest)
			Expect(err).NotTo(HaveOccurred())
			Expect(existingRC).NotTo(Equal(nil))
			fakeReplicationController.GetReturns(existingRC, nil)

			desireAppRequest.NumInstances = 3
		})

		JustBeforeEach(func() {
			if request.Body == nil {
				jsonBytes, err := json.Marshal(&desireAppRequest)
				Expect(err).NotTo(HaveOccurred())
				reader := bytes.NewReader(jsonBytes)

				request.Body = ioutil.NopCloser(reader)
			}

			handler := handlers.NewDesireAppHandler(logger, map[string]recipebuilder.RecipeBuilder{
				"buildpack": buildpackBuilder,
				"docker":    dockerBuilder,
			}, fakeK8s)
			handler.DesireApp(responseRecorder, request)
		})

		It("updates the desired LRP - replication controllers", func() {
			Expect(fakeK8s.ReplicationControllersCallCount()).To(Equal(2))
			Expect(fakeK8s.ReplicationControllersArgsForCall(0)).To(Equal(expectedNamespace))
			Expect(fakeK8s.ReplicationControllersArgsForCall(1)).To(Equal(expectedNamespace))
			Expect(fakeReplicationController.GetCallCount()).To(Equal(1))
			Expect(fakeReplicationController.UpdateCallCount()).To(Equal(1))
			Expect(fakeReplicationController.CreateCallCount()).To(Equal(0))
			actualRC := fakeReplicationController.UpdateArgsForCall(0)
			expectedRC, err := transformer.DesiredAppToRC(logger, desireAppRequest)
			Expect(err).NotTo(HaveOccurred())
			Expect(actualRC).To(Equal(expectedRC))
		})

		It("logs the incoming and outgoing request", func() {
			Eventually(logger.TestSink.Buffer).Should(gbytes.Say("request-from-cc"))
			Eventually(logger.TestSink.Buffer).Should(gbytes.Say("updating-desired-lrp"))
		})

		It("checks to see if LRP already exists", func() {
			Eventually(fakeReplicationController.GetCallCount()).Should(Equal(1))
		})

		It("responds with 202 Accepted", func() {
			Expect(responseRecorder.Code).To(Equal(http.StatusAccepted))
		})

		Context("when the kubernetes fails", func() {
			BeforeEach(func() {
				fakeNamespace = &unversionedfakes.FakeNamespaceInterface{}
				fakeReplicationController = &unversionedfakes.FakeReplicationControllerInterface{}
				fakeK8s.NamespacesReturns(fakeNamespace)
				fakeK8s.ReplicationControllersReturns(fakeReplicationController)
				fakeReplicationController.CreateReturns(nil, errors.New("oh no"))
			})

			It("responds with a ServiceUnavailabe error", func() {
				Expect(responseRecorder.Code).To(Equal(http.StatusServiceUnavailable))
			})
		})

		Context("when the bbs fails with a conflict", func() {
			BeforeEach(func() {
				fakeBBS.UpdateDesiredLRPReturns(models.ErrResourceConflict)
			})

			It("responds with a Conflict error", func() {
				Expect(responseRecorder.Code).To(Equal(http.StatusConflict))
			})
		})

	})

	Context("when an invalid desire app message is received", func() {
		BeforeEach(func() {
			reader := bytes.NewBufferString("not valid json")
			request.Body = ioutil.NopCloser(reader)
		})

		It("does not call the bbs", func() {
			Expect(fakeBBS.RetireActualLRPCallCount()).To(BeZero())
		})

		It("responds with 400 Bad Request", func() {
			Expect(responseRecorder.Code).To(Equal(http.StatusBadRequest))
		})

		It("logs an error", func() {
			Eventually(logger.TestSink.Buffer).Should(gbytes.Say("parse-desired-app-request-failed"))
		})

		It("does not touch the LRP", func() {
			Expect(fakeBBS.DesireLRPCallCount()).To(Equal(0))
			Expect(fakeBBS.UpdateDesiredLRPCallCount()).To(Equal(0))
			Expect(fakeBBS.RemoveDesiredLRPCallCount()).To(Equal(0))
		})
	})

	Context("when the process guids do not match", func() {
		BeforeEach(func() {
			request.Form.Set(":process_guid", "another-guid")
		})

		It("does not call the bbs", func() {
			Expect(fakeBBS.RetireActualLRPCallCount()).To(BeZero())
		})

		It("responds with 400 Bad Request", func() {
			Expect(responseRecorder.Code).To(Equal(http.StatusBadRequest))
		})

		It("logs an error", func() {
			Eventually(logger.TestSink.Buffer).Should(gbytes.Say("desire-app.process-guid-mismatch"))
		})

		It("does not touch the LRP", func() {
			Expect(fakeBBS.DesireLRPCallCount()).To(Equal(0))
			Expect(fakeBBS.UpdateDesiredLRPCallCount()).To(Equal(0))
			Expect(fakeBBS.RemoveDesiredLRPCallCount()).To(Equal(0))
		})
	})
})
