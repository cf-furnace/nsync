package handlers

import (
	"net/http"

	"k8s.io/kubernetes/pkg/client/unversioned"

	"github.com/pivotal-golang/lager"
)

type StopAppHandler struct {
	k8sClient unversioned.Interface
	logger    lager.Logger
}

func NewStopAppHandler(logger lager.Logger, k8sClient unversioned.Interface) *StopAppHandler {
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
		logger.Error("missing-process-guid", missingParameterErr)
		resp.WriteHeader(http.StatusBadRequest)
		return
	}

	logger.Info("serving")
	defer logger.Info("complete")

	logger.Debug("removing-desired-lrp")
	rc, err := h.k8sClient.ReplicationControllers(namespace).Get(processGuid)
	logger.Info("returned rc is ", lager.Data{"rc": rc})
	if rc != nil {
		if err != nil && err.Error() == "replicationcontrollers \""+processGuid+"\" not found" {
			logger.Debug("desired-lrp not found")
			resp.WriteHeader(http.StatusNotFound)
		} else {
			err = h.k8sClient.ReplicationControllers(namespace).Delete(processGuid)
			if err != nil {
				logger.Error("failed-to-remove-desired-lrp", err)

				resp.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			logger.Debug("removed-desired-lrp")

			resp.WriteHeader(http.StatusAccepted)
		}

	} else {
		logger.Info("already deleted, nothing to delete", lager.Data{"process-guid": processGuid})
		resp.WriteHeader(http.StatusNotFound)
		return
	}
}
