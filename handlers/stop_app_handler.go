package handlers

import (
	"net/http"

	"github.com/cloudfoundry-incubator/nsync/helpers"
	"github.com/pivotal-golang/lager"
	"k8s.io/kubernetes/pkg/api"
	kubeerrors "k8s.io/kubernetes/pkg/api/errors"
	v1core "k8s.io/kubernetes/pkg/client/clientset_generated/release_1_3/typed/core/v1"
	"k8s.io/kubernetes/pkg/labels"
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
		logger.Error("missing-process-guid", missingParameterErr)
		resp.WriteHeader(http.StatusBadRequest)
		return
	}

	logger.Info("serving")
	defer logger.Info("complete")

	pg, err := helpers.NewProcessGuid(processGuid)
	if err != nil {
		logger.Error("invalid-process-guid", err)
		resp.WriteHeader(http.StatusBadRequest)
		return
	}

	rcList, err := h.k8sClient.ReplicationControllers(api.NamespaceAll).List(api.ListOptions{
		LabelSelector: labels.Set{"cloudfoundry.org/process-guid": pg.ShortenedGuid()}.AsSelector(),
	})

	if err != nil {
		logger.Error("replication-controller-list-failed", err)
		resp.WriteHeader(http.StatusInternalServerError)
		return
	}

	if len(rcList.Items) == 0 {
		logger.Info("desired-lrp-not-found")
		resp.WriteHeader(http.StatusNotFound)
		return
	}

	for _, rcValue := range rcList.Items {
		rc := &rcValue
		rc.Spec.Replicas = helpers.Int32Ptr(0)

		rc, err = h.k8sClient.ReplicationControllers(rc.ObjectMeta.Namespace).Update(rc)
		if err != nil {
			logger.Error("update-replication-controller-failed", err)
			code := responseCodeFromError(err)
			if code == http.StatusNotFound {
				continue
			}
			resp.WriteHeader(code)
			return
		}

		if err := h.k8sClient.ReplicationControllers(rc.ObjectMeta.Namespace).Delete(rc.Name, nil); err != nil {
			logger.Error("delete-replication-controller-failed", err)
			code := responseCodeFromError(err)
			if code == http.StatusNotFound {
				continue
			}
			resp.WriteHeader(code)
			return
		}
	}

	resp.WriteHeader(http.StatusAccepted)
}

func responseCodeFromError(err error) int {
	switch err := err.(type) {
	case *kubeerrors.StatusError:
		switch err.ErrStatus.Code {
		case http.StatusNotFound:
			return http.StatusNotFound
		default:
			return http.StatusInternalServerError
		}
	default:
		return http.StatusInternalServerError
	}
}
