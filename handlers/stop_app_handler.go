package handlers

import (
	"net/http"

	"github.com/cloudfoundry-incubator/nsync/helpers"
	"github.com/pivotal-golang/lager"
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

	/*if req.Body != nil {
		stopApp := cc_messages.DesireAppRequestFromCC{}
		err := json.NewDecoder(req.Body).Decode(&stopApp)
		if err != nil {
			logger.Error("parse-stop-app-request-failed", err)
			resp.WriteHeader(http.StatusBadRequest)
			return
		}
		logger.Info("stop-request-from-cc", lager.Data{"stop-app": stopApp})
	}*/

	if processGuid == "" {
		h.logger.Error("missing-process-guid", missingParameterErr)
		resp.WriteHeader(http.StatusBadRequest)
		return
	}

	logger.Info("serving")
	defer logger.Info("complete")

	logger.Debug("removing-desired-lrp")

	pg, err := helpers.NewProcessGuid(processGuid)
	if err != nil {
		panic(err)
	}
	rcGUID := pg.ShortenedGuid()
	spaceID := rcGUID

	rc, err := h.k8sClient.ReplicationControllers(spaceID).Get(rcGUID)
	logger.Info("returned rc is ", lager.Data{"rc": rc})
	if rc != nil {
		if err != nil && err.Error() == "replicationcontrollers \""+rcGUID+"\" not found" {
			h.logger.Debug("desired-lrp not found")
			resp.WriteHeader(http.StatusNotFound)
		} else {
			err = h.k8sClient.ReplicationControllers(spaceID).Delete(rcGUID, nil)
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
