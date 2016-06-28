package handlers_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"

	"k8s.io/kubernetes/pkg/api"

	"github.com/cloudfoundry-incubator/bbs/fake_bbs"
	"github.com/cloudfoundry-incubator/nsync/handlers"
	"github.com/cloudfoundry-incubator/nsync/handlers/unversionedfakes"
	"github.com/pivotal-golang/lager/lagertest"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("StopAppHandler", func() {
	var (
		logger                    *lagertest.TestLogger
		fakeBBS                   *fake_bbs.FakeClient
		fakeK8s                   *unversionedfakes.FakeInterface
		fakeNamespace             *unversionedfakes.FakeNamespaceInterface
		fakeReplicationController *unversionedfakes.FakeReplicationControllerInterface
		expectedNamespace         string
		request                   *http.Request
		responseRecorder          *httptest.ResponseRecorder
		apiNS                     *api.Namespace
	)

	BeforeEach(func() {
		logger = lagertest.NewTestLogger("test")
		fakeBBS = &fake_bbs.FakeClient{}
		fakeK8s = &unversionedfakes.FakeInterface{}
		expectedNamespace = "linsun"
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

		responseRecorder = httptest.NewRecorder()

		var err error
		request, err = http.NewRequest("DELETE", "", nil)
		Expect(err).NotTo(HaveOccurred())
		request.Form = url.Values{
			":process_guid": []string{"process-guid"},
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
				fakeReplicationController.GetReturns(&api.ReplicationController{}, nil)
			})

			It("invokes the kubernetes to delete the app", func() {
				Expect(fakeK8s.ReplicationControllersCallCount()).To(Equal(2))
				Expect(fakeK8s.ReplicationControllersArgsForCall(0)).To(Equal(expectedNamespace))
				Expect(fakeK8s.ReplicationControllersArgsForCall(1)).To(Equal(expectedNamespace))
				Expect(fakeReplicationController.DeleteCallCount()).To(Equal(1))
				Expect(fakeReplicationController.DeleteArgsForCall(0)).To(Equal("process-guid"))
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
				fakeReplicationController.GetReturns(nil, errors.New("RC not found"))
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
				fakeReplicationController.GetReturns(&api.ReplicationController{}, nil)
				fakeReplicationController.DeleteReturns(errors.New("oh no"))
			})

			It("responds with a ServiceUnavailabe error", func() {
				Expect(responseRecorder.Code).To(Equal(http.StatusServiceUnavailable))
			})
		})

	})
})
