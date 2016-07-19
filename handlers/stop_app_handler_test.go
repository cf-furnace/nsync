package handlers_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"

	"k8s.io/kubernetes/pkg/api/v1"

	"github.com/cloudfoundry-incubator/nsync/handlers"
	"github.com/cloudfoundry-incubator/nsync/handlers/fakes"
	"github.com/cloudfoundry-incubator/nsync/helpers"
	"github.com/nu7hatch/gouuid"
	"github.com/pivotal-golang/lager/lagertest"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("StopAppHandler", func() {
	var (
		logger                    *lagertest.TestLogger
		fakeK8s                   *fakes.FakeKubeClient
		fakeNamespace             *fakes.FakeNamespace
		fakeReplicationController *fakes.FakeReplicationController

		request          *http.Request
		responseRecorder *httptest.ResponseRecorder

		processGuid       helpers.ProcessGuid
		expectedNamespace string
		apiNS             *v1.Namespace
	)

	BeforeEach(func() {
		logger = lagertest.NewTestLogger("test")
		fakeK8s = &fakes.FakeKubeClient{}
		fakeNamespace = &fakes.FakeNamespace{}
		fakeReplicationController = &fakes.FakeReplicationController{}

		appGuid, err := uuid.NewV4()
		Expect(err).NotTo(HaveOccurred())
		appVersion, err := uuid.NewV4()
		Expect(err).NotTo(HaveOccurred())

		pg := appGuid.String() + "-" + appVersion.String()
		processGuid, err = helpers.NewProcessGuid(pg)
		Expect(err).NotTo(HaveOccurred())

		expectedNamespace = processGuid.ShortenedGuid()
		apiNS = &v1.Namespace{
			ObjectMeta: v1.ObjectMeta{
				Name: expectedNamespace,
			},
			Spec: v1.NamespaceSpec{
				Finalizers: []v1.FinalizerName{v1.FinalizerName(expectedNamespace)},
			},
		}

		responseRecorder = httptest.NewRecorder()

		request, err = http.NewRequest("DELETE", "", nil)
		Expect(err).NotTo(HaveOccurred())
		request.Form = url.Values{
			":process_guid": []string{processGuid.String()},
		}
	})

	JustBeforeEach(func() {
		stopAppHandler := handlers.NewStopAppHandler(logger, fakeK8s)
		stopAppHandler.StopApp(responseRecorder, request)
	})

	Context("when deleting the desired app", func() {
		Context("when the desired app exists", func() {
			BeforeEach(func() {
				fakeK8s.NamespacesReturns(fakeNamespace)
				fakeNamespace.GetReturns(apiNS, nil)
				fakeK8s.ReplicationControllersReturns(fakeReplicationController)
				fakeReplicationController.GetReturns(&v1.ReplicationController{}, nil)
			})

			It("invokes the kubernetes to delete the app", func() {
				Expect(fakeK8s.ReplicationControllersCallCount()).To(Equal(2))
				Expect(fakeK8s.ReplicationControllersArgsForCall(0)).To(Equal(expectedNamespace))
				Expect(fakeK8s.ReplicationControllersArgsForCall(1)).To(Equal(expectedNamespace))
				Expect(fakeReplicationController.DeleteCallCount()).To(Equal(1))
				Expect(fakeReplicationController.DeleteArgsForCall(0)).To(Equal(processGuid.ShortenedGuid()))
			})

			It("responds with 202 Accepted", func() {
				Expect(responseRecorder.Code).To(Equal(http.StatusAccepted))
			})
		})

		Context("when the desired app doesn't exist", func() {
			BeforeEach(func() {
				fakeK8s.NamespacesReturns(fakeNamespace)
				fakeNamespace.GetReturns(apiNS, nil)
				fakeK8s.ReplicationControllersReturns(fakeReplicationController)
				fakeReplicationController.GetReturns(nil, errors.New("replicationcontrollers \"process-guid\" not found"))
			})

			It("responds with a 404", func() {
				Expect(fakeReplicationController.DeleteCallCount()).To(Equal(0))
				Expect(responseRecorder.Code).To(Equal(http.StatusNotFound))
			})
		})

		Context("when process-guid is missing in the request", func() {
			BeforeEach(func() {
				request.Form.Del(":process_guid")
			})

			It("does not call the kubernetes", func() {
				Expect(fakeK8s.ReplicationControllersCallCount()).To(Equal(0))
			})

			It("responds with 400 Bad Request", func() {
				Expect(responseRecorder.Code).To(Equal(http.StatusBadRequest))
			})
		})

		Context("when kubernetes failed", func() {
			BeforeEach(func() {
				fakeK8s.NamespacesReturns(fakeNamespace)
				fakeK8s.ReplicationControllersReturns(fakeReplicationController)
				fakeReplicationController.GetReturns(&v1.ReplicationController{}, nil)
				fakeReplicationController.DeleteReturns(errors.New("oh no"))
			})

			It("responds with a ServiceUnavailabe error", func() {
				Expect(responseRecorder.Code).To(Equal(http.StatusServiceUnavailable))
			})
		})
	})
})
