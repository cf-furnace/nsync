package handlers_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"

	"k8s.io/kubernetes/pkg/api"
	kubeerrors "k8s.io/kubernetes/pkg/api/errors"
	"k8s.io/kubernetes/pkg/api/unversioned"
	"k8s.io/kubernetes/pkg/api/v1"
	"k8s.io/kubernetes/pkg/labels"

	"github.com/cloudfoundry-incubator/nsync/handlers"
	"github.com/cloudfoundry-incubator/nsync/handlers/fakes"
	"github.com/cloudfoundry-incubator/nsync/helpers"
	"github.com/nu7hatch/gouuid"
	"github.com/pivotal-golang/lager/lagertest"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
)

var _ = Describe("StopAppHandler", func() {
	var (
		logger                    *lagertest.TestLogger
		fakeClient                *fakes.FakeKubeClient
		fakeReplicationController *fakes.FakeReplicationController

		processGuid               helpers.ProcessGuid
		replicationControllerList *v1.ReplicationControllerList

		request *http.Request
		resp    *httptest.ResponseRecorder
	)

	BeforeEach(func() {
		logger = lagertest.NewTestLogger("test")
		appGuid, err := uuid.NewV4()
		Expect(err).NotTo(HaveOccurred())
		appVersion, err := uuid.NewV4()
		Expect(err).NotTo(HaveOccurred())

		pg := appGuid.String() + "-" + appVersion.String()
		processGuid, err = helpers.NewProcessGuid(pg)
		Expect(err).NotTo(HaveOccurred())

		replicationControllerList = &v1.ReplicationControllerList{
			Items: []v1.ReplicationController{{
				ObjectMeta: v1.ObjectMeta{
					Name:      processGuid.ShortenedGuid(),
					Namespace: "space-guid",
					Labels: map[string]string{
						"cloudfoundry.org/process-guid": processGuid.ShortenedGuid(),
					},
				},
				Spec: v1.ReplicationControllerSpec{
					Replicas: helpers.Int32Ptr(999),
				},
			}},
		}

		fakeClient = &fakes.FakeKubeClient{}
		fakeReplicationController = &fakes.FakeReplicationController{}
		fakeClient.ReplicationControllersReturns(fakeReplicationController)
		fakeReplicationController.ListReturns(replicationControllerList, nil)

		fakeReplicationController.UpdateStub = func(rc *v1.ReplicationController) (*v1.ReplicationController, error) {
			return rc, nil
		}

		resp = httptest.NewRecorder()
		request, err = http.NewRequest("DELETE", "", nil)
		Expect(err).NotTo(HaveOccurred())

		request.Form = url.Values{
			":process_guid": []string{processGuid.String()},
		}
	})

	JustBeforeEach(func() {
		stopAppHandler := handlers.NewStopAppHandler(logger, fakeClient)
		stopAppHandler.StopApp(resp, request)
	})

	It("lists replication controllers with the matching process guid", func() {
		Expect(fakeClient.ReplicationControllersCallCount()).To(BeNumerically(">", 1))
		Expect(fakeClient.ReplicationControllersArgsForCall(0)).To(Equal(api.NamespaceAll))

		Expect(fakeReplicationController.ListCallCount()).To(Equal(1))
		Expect(fakeReplicationController.ListArgsForCall(0)).To(Equal(api.ListOptions{
			LabelSelector: labels.Set{"cloudfoundry.org/process-guid": processGuid.ShortenedGuid()}.AsSelector(),
		}))
	})

	Context("when a replication controller is present", func() {
		BeforeEach(func() {
			Expect(replicationControllerList.Items).To(HaveLen(1))
			Expect(replicationControllerList.Items[0].Spec.Replicas).NotTo(BeNil())
			Expect(*replicationControllerList.Items[0].Spec.Replicas).To(BeNumerically(">", 0))

			fakeReplicationController.UpdateStub = func(rc *v1.ReplicationController) (*v1.ReplicationController, error) {
				Expect(fakeReplicationController.DeleteCallCount()).To(Equal(0))
				return rc, nil
			}
		})

		It("sets the pod replica count to zero before deleting", func() {
			Expect(fakeReplicationController.UpdateCallCount()).To(Equal(1))

			rc := fakeReplicationController.UpdateArgsForCall(0)
			Expect(rc.Spec.Replicas).NotTo(BeNil())
			Expect(*rc.Spec.Replicas).To(BeEquivalentTo(0))
		})

		It("deletes the replication controller", func() {
			Expect(fakeReplicationController.DeleteCallCount()).To(Equal(1))

			name, opts := fakeReplicationController.DeleteArgsForCall(0)
			Expect(name).To(Equal(processGuid.ShortenedGuid()))
			Expect(opts).To(BeNil())
		})

		Context("when updating the replication controller fails", func() {
			Context("because the replication controller is not found", func() {
				BeforeEach(func() {
					fakeReplicationController.UpdateReturns(nil, &kubeerrors.StatusError{
						ErrStatus: unversioned.Status{
							Message: "replication controller not found",
							Status:  unversioned.StatusFailure,
							Reason:  unversioned.StatusReasonNotFound,
							Code:    http.StatusNotFound,
						},
					})
				})

				It("logs the error", func() {
					Eventually(logger).Should(gbytes.Say("update-replication-controller-failed.*replication controller not found"))
				})

				It("keeps calm and carries on", func() {
					Expect(resp.Code).To(Equal(http.StatusAccepted))
				})
			})

			Context("with an unexpected error", func() {
				BeforeEach(func() {
					fakeReplicationController.UpdateReturns(nil, &kubeerrors.StatusError{
						ErrStatus: unversioned.Status{
							Message: "potato",
							Status:  unversioned.StatusFailure,
							Reason:  unversioned.StatusReasonInternalError,
							Code:    http.StatusInternalServerError,
						},
					})
				})

				It("logs the error", func() {
					Eventually(logger).Should(gbytes.Say("update-replication-controller-failed.*potato"))
				})

				It("reponds with an internal server error 500", func() {
					Expect(resp.Code).To(Equal(http.StatusInternalServerError))
				})
			})

			Context("with an unknown error type", func() {
				BeforeEach(func() {
					fakeReplicationController.UpdateReturns(nil, errors.New("tomato"))
				})

				It("logs the error", func() {
					Eventually(logger).Should(gbytes.Say("update-replication-controller-failed.*tomato"))
				})

				It("reponds with an internal server error 500", func() {
					Expect(resp.Code).To(Equal(http.StatusInternalServerError))
				})
			})
		})

		Context("when deleting a replication controller fails", func() {
			Context("because the replication controller is not found", func() {
				BeforeEach(func() {
					fakeReplicationController.DeleteReturns(&kubeerrors.StatusError{
						ErrStatus: unversioned.Status{
							Message: "replication controller not found",
							Status:  unversioned.StatusFailure,
							Reason:  unversioned.StatusReasonNotFound,
							Code:    http.StatusNotFound,
						},
					})
				})

				It("logs the error", func() {
					Eventually(logger).Should(gbytes.Say("delete-replication-controller-failed.*replication controller not found"))
				})

				It("keeps calm and carries on", func() {
					Expect(resp.Code).To(Equal(http.StatusAccepted))
				})
			})

			Context("with an unexpected error", func() {
				BeforeEach(func() {
					fakeReplicationController.DeleteReturns(&kubeerrors.StatusError{
						ErrStatus: unversioned.Status{
							Message: "potato",
							Status:  unversioned.StatusFailure,
							Reason:  unversioned.StatusReasonInternalError,
							Code:    http.StatusInternalServerError,
						},
					})
				})

				It("logs the error", func() {
					Eventually(logger).Should(gbytes.Say("delete-replication-controller-failed.*potato"))
				})

				It("reponds with an internal server error 500", func() {
					Expect(resp.Code).To(Equal(http.StatusInternalServerError))
				})
			})

			Context("with an unknown error type", func() {
				BeforeEach(func() {
					fakeReplicationController.DeleteReturns(errors.New("tomato"))
				})

				It("logs the error", func() {
					Eventually(logger).Should(gbytes.Say("delete-replication-controller-failed.*tomato"))
				})

				It("reponds with an internal server error 500", func() {
					Expect(resp.Code).To(Equal(http.StatusInternalServerError))
				})
			})
		})
	})

	Context("when parsing the process guid fails", func() {
		BeforeEach(func() {
			request.Form = url.Values{
				":process_guid": []string{"some-goo-that-is-bad"},
			}
		})

		It("logs the error", func() {
			Eventually(logger).Should(gbytes.Say("invalid-process-guid.*some-goo-that-is-bad"))
		})

		It("does not attempt to list the replication controllers", func() {
			Expect(fakeClient.ReplicationControllersCallCount()).To(Equal(0))
		})

		It("responds with a Bad Request 400", func() {
			Expect(resp.Code).To(Equal(http.StatusBadRequest))
		})
	})

	Context("when listing replication controllers fails", func() {
		BeforeEach(func() {
			fakeReplicationController.ListReturns(nil, errors.New("woops"))
		})

		It("reponds with an internal server error 500", func() {
			Expect(resp.Code).To(Equal(http.StatusInternalServerError))
		})

		It("logs the error", func() {
			Eventually(logger).Should(gbytes.Say("replication-controller-list-failed.*woops"))
		})
	})

	Context("when no replication controllers are found", func() {
		BeforeEach(func() {
			fakeReplicationController.ListReturns(
				&v1.ReplicationControllerList{Items: []v1.ReplicationController{}},
				nil,
			)
		})

		It("reponds with a not found 404", func() {
			Expect(resp.Code).To(Equal(http.StatusNotFound))
		})

		It("logs the error", func() {
			Eventually(logger).Should(gbytes.Say("desired-lrp-not-found.*process-guid.*" + processGuid.String()))
		})
	})
})
