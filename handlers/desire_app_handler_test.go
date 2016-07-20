package handlers_test

import (
	"bytes"
	"encoding/json"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"net/url"

	"github.com/cloudfoundry-incubator/bbs/models"
	"github.com/cloudfoundry-incubator/nsync/handlers"
	"github.com/cloudfoundry-incubator/nsync/handlers/fakes"
	"github.com/cloudfoundry-incubator/nsync/handlers/transformer"
	"github.com/cloudfoundry-incubator/nsync/helpers"
	"github.com/cloudfoundry-incubator/runtime-schema/cc_messages"
	"github.com/cloudfoundry/dropsonde/metric_sender/fake"
	"github.com/cloudfoundry/dropsonde/metrics"
	"github.com/nu7hatch/gouuid"
	"github.com/pivotal-golang/lager/lagertest"

	kubeerrors "k8s.io/kubernetes/pkg/api/errors"
	"k8s.io/kubernetes/pkg/api/unversioned"
	"k8s.io/kubernetes/pkg/api/v1"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
)

var _ = Describe("DesireAppHandler", func() {
	var (
		logger           *lagertest.TestLogger
		desireAppRequest cc_messages.DesireAppRequestFromCC
		processGuid      helpers.ProcessGuid
		metricSender     *fake.FakeMetricSender

		request          *http.Request
		responseRecorder *httptest.ResponseRecorder

		fakeKubeClient            *fakes.FakeKubeClient
		fakeNamespace             *fakes.FakeNamespace
		fakeReplicationController *fakes.FakeReplicationController
	)

	BeforeEach(func() {
		logger = lagertest.NewTestLogger("test")

		fakeNamespace = &fakes.FakeNamespace{}
		fakeNamespace.GetReturns(&v1.Namespace{
			ObjectMeta: v1.ObjectMeta{
				Name: "my-space-id",
			},
		}, nil)

		fakeReplicationController = &fakes.FakeReplicationController{}
		fakeReplicationController.GetReturns(nil, &kubeerrors.StatusError{
			ErrStatus: unversioned.Status{
				Status: unversioned.StatusFailure,
				Reason: unversioned.StatusReasonNotFound,
				Code:   http.StatusNotFound,
			},
		})

		fakeKubeClient = &fakes.FakeKubeClient{}
		fakeKubeClient.NamespacesReturns(fakeNamespace)
		fakeKubeClient.ReplicationControllersReturns(fakeReplicationController)

		routingInfo, err := cc_messages.CCHTTPRoutes{
			{Hostname: "route1"},
			{Hostname: "route2"},
		}.CCRouteInfo()
		Expect(err).NotTo(HaveOccurred())

		appGuid, err := uuid.NewV4()
		Expect(err).NotTo(HaveOccurred())
		appVersion, err := uuid.NewV4()
		Expect(err).NotTo(HaveOccurred())

		pg := appGuid.String() + "-" + appVersion.String()
		processGuid, err = helpers.NewProcessGuid(pg)
		Expect(err).NotTo(HaveOccurred())

		desireAppRequest = cc_messages.DesireAppRequestFromCC{
			ProcessGuid:  pg,
			DropletUri:   "http://the-droplet.uri.com",
			Stack:        "some-stack",
			StartCommand: "the-start-command",
			Environment: []*models.EnvironmentVariable{
				{Name: "foo", Value: "bar"},
				{Name: "VCAP_APPLICATION", Value: `{
					"application_name":"my-app",
					"space_id":"my-space-id",
					"application_id":
					"my-very-long-application-id"
				}`},
				{Name: "VCAP_SERVICES", Value: "{}"},
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
			":process_guid": []string{pg},
		}
	})

	JustBeforeEach(func() {
		if request.Body == nil {
			jsonBytes, err := json.Marshal(&desireAppRequest)
			Expect(err).NotTo(HaveOccurred())
			reader := bytes.NewReader(jsonBytes)

			request.Body = ioutil.NopCloser(reader)
		}

		handler := handlers.NewDesireAppHandler(logger, fakeKubeClient)
		handler.DesireApp(responseRecorder, request)
	})

	Context("when the namespace is missing", func() {
		BeforeEach(func() {
			err := &kubeerrors.StatusError{
				ErrStatus: unversioned.Status{
					Status: unversioned.StatusFailure,
					Reason: unversioned.StatusReasonNotFound,
					Code:   http.StatusNotFound,
				},
			}
			fakeNamespace.GetReturns(nil, err)
		})

		It("creates the namespace", func() {
			Expect(fakeNamespace.GetCallCount()).To(Equal(1))
			Expect(fakeNamespace.GetArgsForCall(0)).To(Equal("my-space-id"))

			Expect(fakeNamespace.CreateCallCount()).To(Equal(1))
			Expect(fakeNamespace.CreateArgsForCall(0)).To(Equal(&v1.Namespace{
				ObjectMeta: v1.ObjectMeta{Name: "my-space-id"},
			}))
		})
	})

	Context("when the replciation controller for the app is missing", func() {
		It("creates a replication controllers", func() {
			Expect(fakeKubeClient.ReplicationControllersCallCount()).To(Equal(2))
			Expect(fakeKubeClient.ReplicationControllersArgsForCall(0)).To(Equal("my-space-id"))
			Expect(fakeKubeClient.ReplicationControllersArgsForCall(1)).To(Equal("my-space-id"))

			Expect(fakeReplicationController.GetCallCount()).To(Equal(1))
			Expect(fakeReplicationController.GetArgsForCall(0)).To(Equal(processGuid.ShortenedGuid()))

			expectedRC, err := transformer.DesiredAppToRC(logger, processGuid, desireAppRequest)
			Expect(err).NotTo(HaveOccurred())

			Expect(fakeReplicationController.CreateCallCount()).To(Equal(1))
			Expect(fakeReplicationController.CreateArgsForCall(0)).To(Equal(expectedRC))
		})

		It("responds with 202 Accepted", func() {
			Expect(responseRecorder.Code).To(Equal(http.StatusAccepted))
		})

		It("increments the desired LRPs counter", func() {
			Expect(metricSender.GetCounter("LRPsDesired")).To(Equal(uint64(1)))
		})

		Context("when creaating the replication controller fails", func() {
			BeforeEach(func() {
				fakeReplicationController.CreateReturns(nil, &kubeerrors.StatusError{
					ErrStatus: unversioned.Status{
						Status: unversioned.StatusFailure,
						Reason: unversioned.StatusReasonInternalError,
						Code:   http.StatusInternalServerError,
					},
				})
			})

			It("responds with 500 Internal Server Error", func() {
				Expect(responseRecorder.Code).To(Equal(http.StatusInternalServerError))
			})

			It("logs an error", func() {
				Eventually(logger).Should(gbytes.Say("desire-app.create-replication-controller.failed-to-create"))
			})

			It("does not increment the desired LRPs counter", func() {
				Expect(metricSender.GetCounter("LRPsDesired")).To(Equal(uint64(0)))
			})
		})
	})

	Context("when the replication controller already exists", func() {
		var expectedRC *v1.ReplicationController

		BeforeEach(func() {
			expectedRC = &v1.ReplicationController{
				ObjectMeta: v1.ObjectMeta{Name: processGuid.ShortenedGuid()},
				Spec: v1.ReplicationControllerSpec{
					Replicas: helpers.Int32Ptr(2),
				},
			}

			existingRC := expectedRC
			existingRC.Spec.Replicas = helpers.Int32Ptr(10)
			fakeReplicationController.GetReturns(existingRC, nil)
		})

		It("updates the replication controller", func() {
			Expect(fakeReplicationController.UpdateCallCount()).To(Equal(1))
			Expect(fakeReplicationController.UpdateArgsForCall(0)).To(Equal(expectedRC))
		})

		It("responds with 202 Accepted", func() {
			Expect(responseRecorder.Code).To(Equal(http.StatusAccepted))
		})

		It("increments the desired LRPs counter", func() {
			Expect(metricSender.GetCounter("LRPsDesired")).To(Equal(uint64(1)))
		})

		Context("when updating the replication controller fails", func() {
			BeforeEach(func() {
				fakeReplicationController.UpdateReturns(nil, &kubeerrors.StatusError{
					ErrStatus: unversioned.Status{
						Status: unversioned.StatusFailure,
						Reason: unversioned.StatusReasonInternalError,
						Code:   http.StatusInternalServerError,
					},
				})
			})

			It("responds with 500 Internal Server Error", func() {
				Expect(responseRecorder.Code).To(Equal(http.StatusInternalServerError))
			})

			It("logs an error", func() {
				Eventually(logger).Should(gbytes.Say("desire-app.update-replication-controller.failed-to-update"))
			})

			It("does not increment the desired LRPs counter", func() {
				Expect(metricSender.GetCounter("LRPsDesired")).To(Equal(uint64(0)))
			})
		})
	})

	Context("when an invalid desire app message is received", func() {
		BeforeEach(func() {
			reader := bytes.NewBufferString("not valid json")
			request.Body = ioutil.NopCloser(reader)
		})

		It("does not create or update a replication controller", func() {
			Expect(fakeReplicationController.UpdateCallCount()).To(Equal(0))
		})

		It("responds with 400 Bad Request", func() {
			Expect(responseRecorder.Code).To(Equal(http.StatusBadRequest))
		})

		It("logs an error", func() {
			Eventually(logger).Should(gbytes.Say("parse-desired-app-request-failed"))
		})
	})

	Context("when the process guids do not match", func() {
		BeforeEach(func() {
			request.Form.Set(":process_guid", "another-guid")
		})

		It("does not create or update a replication controller", func() {
			Expect(fakeReplicationController.UpdateCallCount()).To(Equal(0))
		})

		It("responds with 400 Bad Request", func() {
			Expect(responseRecorder.Code).To(Equal(http.StatusBadRequest))
		})

		It("logs an error", func() {
			Eventually(logger).Should(gbytes.Say("desire-app.process-guid-mismatch"))
		})
	})

	Context("when the space guid is missing from VCAP_APPLICATION", func() {
		BeforeEach(func() {
			desireAppRequest.Environment = []*models.EnvironmentVariable{
				{Name: "foo", Value: "bar"},
				{Name: "VCAP_APPLICATION", Value: `{
					"application_name":"my-app",
					"application_id":
					"my-very-long-application-id"
				}`},
				{Name: "VCAP_SERVICES", Value: "{}"},
			}
		})

		It("responds with a 400 Bad Request", func() {
			Expect(responseRecorder.Code).To(Equal(http.StatusBadRequest))
		})

		It("logs an error", func() {
			Eventually(logger).Should(gbytes.Say("desire-app.missing-space-guid"))
		})
	})

	Context("when creating a namespace fails", func() {
		BeforeEach(func() {
			fakeNamespace.GetReturns(nil, &kubeerrors.StatusError{
				ErrStatus: unversioned.Status{
					Status: unversioned.StatusFailure,
					Reason: unversioned.StatusReasonNotFound,
					Code:   http.StatusNotFound,
				},
			})
			fakeNamespace.CreateReturns(nil, &kubeerrors.StatusError{
				ErrStatus: unversioned.Status{
					Status: unversioned.StatusFailure,
					Reason: unversioned.StatusReasonInternalError,
					Code:   http.StatusInternalServerError,
				},
			})
		})

		It("responds with 500 Internal Server Error", func() {
			Expect(responseRecorder.Code).To(Equal(http.StatusInternalServerError))
		})

		It("logs an error", func() {
			Eventually(logger).Should(gbytes.Say("desire-app.find-or-create-namespace"))
		})
	})

	Context("when the process guid cannot be parsed", func() {
		BeforeEach(func() {
			desireAppRequest.ProcessGuid = "bogus-process-guid"
			request.Form = url.Values{":process_guid": []string{"bogus-process-guid"}}
		})

		It("responds with 400 Bad Request", func() {
			Expect(responseRecorder.Code).To(Equal(http.StatusBadRequest))
		})

		It("logs an error", func() {
			Eventually(logger).Should(gbytes.Say("desire-app.new-process-guid"))
		})
	})

})
