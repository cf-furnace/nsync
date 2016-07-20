package handlers

import (
	"net/http"

	"github.com/cloudfoundry-incubator/nsync/helpers"
	"github.com/pivotal-golang/lager"
	"k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/api/v1"
	v1core "k8s.io/kubernetes/pkg/client/clientset_generated/release_1_3/typed/core/v1"
)

type StopAppHandler struct {
	k8sClient v1core.CoreInterface
	logger    lager.Logger
}

func NewStopAppHandler(logger lager.Logger, k8sClient v1core.CoreInterface) *StopAppHandler {
	return &StopAppHandler{
		logger:    logger,
		k8sClient: k8sClient,
	}
}

func (h *StopAppHandler) StopApp(resp http.ResponseWriter, req *http.Request) {
	processGuid := req.FormValue(":process_guid")

	logger := h.logger.Session("stop-app", lager.Data{
		"process-guid": processGuid,
		"method":       req.Method,
		"request":      req.URL.String(),
	})

	if processGuid == "" {
		h.logger.Error("missing-process-guid", missingParameterErr)
		resp.WriteHeader(http.StatusBadRequest)
		return
	}

	logger.Info("serving")
	defer logger.Info("complete")

	logger.Debug("removing-desired-lrp")

	var rc *v1.ReplicationController
	//var err error

	pg, err := helpers.NewProcessGuid(processGuid)
	if err != nil {
		panic(err)
	}
	rcGUID := pg.ShortenedGuid()
	spaceID := rcGUID

	rc, err = h.k8sClient.ReplicationControllers(spaceID).Get(rcGUID)
	logger.Info("returned rc is ", lager.Data{"rc": rc})
	if rc != nil {
		if err != nil && err.Error() == "replicationcontrollers \""+rcGUID+"\" not found" {
			h.logger.Debug("desired-lrp not found")
			resp.WriteHeader(http.StatusNotFound)
		} else {
			if err != nil && err.Error() == "replicationcontrollers found but no pods" {
				// ignore:  tests with fake replicationControllers
				h.logger.Debug("ignore deleting pod")
			} else if err != nil {
				// return service unavailable on all other err
				h.logger.Error("error-check-rc-exist", err)
				resp.WriteHeader(http.StatusServiceUnavailable)
				return
			} else {
				h.logger.Error("error-check-rc-not-exist", err)
				podSpec := &rc.Spec.Template.Spec
				for _, element := range podSpec.Containers {
					h.k8sClient.Pods(spaceID).Delete(element.Name, &api.DeleteOptions{})
				}
			}

			err := h.k8sClient.ReplicationControllers(spaceID).Delete(rcGUID, nil)

			if err != nil {
				h.logger.Error("failed-to-remove-desired-lrp", err)

				resp.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			h.logger.Debug("removed-desired-lrp")

			resp.WriteHeader(http.StatusAccepted)
		}

	} else {
		h.logger.Info("already deleted, nothing to delete", lager.Data{"process-guid": processGuid})
		resp.WriteHeader(http.StatusNotFound)
		return
	}
}
