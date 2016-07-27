package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/cloudfoundry-incubator/bbs/models"
	"github.com/cloudfoundry-incubator/nsync/helpers"
	"github.com/cloudfoundry-incubator/nsync/recipebuilder"
	"github.com/cloudfoundry-incubator/routing-info/cfroutes"
	"github.com/cloudfoundry-incubator/routing-info/tcp_routes"
	"github.com/cloudfoundry-incubator/runtime-schema/cc_messages"
	"github.com/cloudfoundry-incubator/runtime-schema/metric"
	"github.com/pivotal-golang/lager"

	kubeerrors "k8s.io/kubernetes/pkg/api/errors"
	"k8s.io/kubernetes/pkg/api/unversioned"
	"k8s.io/kubernetes/pkg/api/v1"
	v1core "k8s.io/kubernetes/pkg/client/clientset_generated/release_1_3/typed/core/v1"
)

const (
	desiredLRPCounter = metric.Counter("LRPsDesired")
)

type DesireAppHandler struct {
	logger         lager.Logger
	recipeBuilders map[string]recipebuilder.FurnaceRecipeBuilder
	k8sClient      v1core.CoreInterface
}

func NewDesireAppHandler(logger lager.Logger, builders map[string]recipebuilder.FurnaceRecipeBuilder, k8sClient v1core.CoreInterface) *DesireAppHandler {
	return &DesireAppHandler{
		logger:         logger,
		recipeBuilders: builders,
		k8sClient:      k8sClient,
	}
}

func (h *DesireAppHandler) DesireApp(resp http.ResponseWriter, req *http.Request) {
	processGuid := req.FormValue(":process_guid")

	logger := h.logger.Session("desire-app", lager.Data{
		"process_guid": processGuid,
		"method":       req.Method,
		"request":      req.URL.String(),
	})

	logger.Info("serving")
	defer logger.Info("complete")

	desiredApp := cc_messages.DesireAppRequestFromCC{}
	err := json.NewDecoder(req.Body).Decode(&desiredApp)
	if err != nil {
		logger.Error("parse-desired-app-request-failed", err)
		resp.WriteHeader(http.StatusBadRequest)
		return
	}
	logger.Info("request-from-cc", lager.Data{"desired-app": desiredApp})

	if processGuid != desiredApp.ProcessGuid {
		logger.Error("process-guid-mismatch", err, lager.Data{"body-process-guid": desiredApp.ProcessGuid})
		resp.WriteHeader(http.StatusBadRequest)
		return
	}

	spaceGuid, err := recipebuilder.GetSpaceGuid(desiredApp)
	if err != nil || spaceGuid == "" {
		logger.Error("missing-space-guid", err)
		resp.WriteHeader(http.StatusBadRequest)
		return
	}

	if err := h.findOrCreateNamespace(logger, spaceGuid); err != nil {
		logger.Error("find-or-create-namespace", err)
		resp.WriteHeader(http.StatusInternalServerError)
		return
	}

	procGuid, err := helpers.NewProcessGuid(processGuid)
	if err != nil {
		logger.Error("new-process-guid", err)
		resp.WriteHeader(http.StatusBadRequest)
		return
	}

	replicationController, err := h.k8sClient.ReplicationControllers(spaceGuid).Get(procGuid.ShortenedGuid())
	if err == nil {
		err = h.updateReplicationController(logger, replicationController, desiredApp)
	} else {
		err = h.createReplicationController(logger, &desiredApp, spaceGuid)
	}

	if err == nil {
		desiredLRPCounter.Increment()
		resp.WriteHeader(http.StatusAccepted)
	} else {
		resp.WriteHeader(http.StatusInternalServerError)
	}
}

func (h *DesireAppHandler) findOrCreateNamespace(logger lager.Logger, namespace string) error {
	_, err := h.k8sClient.Namespaces().Get(namespace)
	if err == nil {
		return nil
	}

	_, err = h.k8sClient.Namespaces().Create(&v1.Namespace{
		ObjectMeta: v1.ObjectMeta{Name: namespace},
	})
	if err == nil {
		return nil
	}

	if statusError, ok := err.(*kubeerrors.StatusError); ok {
		if statusError.Status().Reason == unversioned.StatusReasonAlreadyExists {
			return nil
		}
	}
	return err
}

func (h *DesireAppHandler) createReplicationController(
	logger lager.Logger,
	desiredApp *cc_messages.DesireAppRequestFromCC,
	namespace string,
) error {
	logger = logger.Session("create-replication-controller")

	builder := h.recipeBuilders["buildpack"]
	if desiredApp.DockerImageUrl != "" {
		builder = h.recipeBuilders["docker"]
	}

	replicationController, err := builder.BuildReplicationController(desiredApp)
	if err != nil {
		logger.Fatal("failed-to-transform-desired-app-to-rc", err)
	}

	_, err = h.k8sClient.ReplicationControllers(namespace).Create(replicationController)
	if err != nil {
		logger.Error("failed-to-create", err)
		return err
	}

	return nil
}

func (h *DesireAppHandler) updateReplicationController(
	logger lager.Logger,
	replicationController *v1.ReplicationController,
	desireAppMessage cc_messages.DesireAppRequestFromCC,
) error {
	logger = logger.Session("update-replication-controller")

	// Diego allows updates on instances, routes, and annotations
	replicationController.Spec.Replicas = helpers.Int32Ptr(desireAppMessage.NumInstances)

	_, err := h.k8sClient.ReplicationControllers(replicationController.ObjectMeta.Namespace).Update(replicationController)
	if err != nil {
		logger.Error("failed-to-update", err)
		return err
	}

	return nil
}

func sanitizeRoutes(routes *models.Routes) *models.Routes {
	newRoutes := make(models.Routes)
	if routes != nil {
		cfRoutes := *routes
		newRoutes[cfroutes.CF_ROUTER] = cfRoutes[cfroutes.CF_ROUTER]
		newRoutes[tcp_routes.TCP_ROUTER] = cfRoutes[tcp_routes.TCP_ROUTER]
	}
	return &newRoutes
}
