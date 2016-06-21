package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/cf-furnace/nsync/handlers/transformer"
	"github.com/cf-furnace/nsync/recipebuilder"
	"github.com/cloudfoundry-incubator/bbs/models"
	"github.com/cloudfoundry-incubator/routing-info/cfroutes"
	"github.com/cloudfoundry-incubator/routing-info/tcp_routes"
	"github.com/cloudfoundry-incubator/runtime-schema/cc_messages"
	"github.com/cloudfoundry-incubator/runtime-schema/metric"
	"github.com/pivotal-golang/lager"
	"k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/client/unversioned"
)

const (
	desiredLRPCounter = metric.Counter("LRPsDesired")
	namespace         = "linsun" // TODO need to figure out how to get namespace
)

type DesireAppHandler struct {
	recipeBuilders map[string]recipebuilder.RecipeBuilder
	logger         lager.Logger
	k8sClient      unversioned.Client
}

func NewDesireAppHandler(logger lager.Logger, builders map[string]recipebuilder.RecipeBuilder, k8sClient *unversioned.Client) DesireAppHandler {
	return DesireAppHandler{
		recipeBuilders: builders,
		logger:         logger,
		k8sClient:      *k8sClient,
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
	logger.Info("request-from-cc", lager.Data{"routing_info": desiredApp.RoutingInfo})

	envNames := []string{}
	for _, envVar := range desiredApp.Environment {
		envNames = append(envNames, envVar.Name)
	}
	logger.Debug("environment", lager.Data{"keys": envNames})

	if processGuid != desiredApp.ProcessGuid {
		logger.Error("process-guid-mismatch", err, lager.Data{"body-process-guid": desiredApp.ProcessGuid})
		resp.WriteHeader(http.StatusBadRequest)
		return
	}

	statusCode := http.StatusConflict
	for tries := 2; tries > 0 && statusCode == http.StatusConflict; tries-- {
		existingRC, err := h.getDesiredRC(logger, processGuid)
		if err != nil {
			statusCode = http.StatusServiceUnavailable
			break
		}

		if existingRC != nil {
			err = h.updateDesiredApp(logger, existingRC, desiredApp)
		} else {
			err = h.createDesiredApp(logger, desiredApp)
		}

		if err != nil {
			mErr := models.ConvertError(err)
			switch mErr.Type {
			case models.Error_ResourceConflict:
				fallthrough
			case models.Error_ResourceExists:
				statusCode = http.StatusConflict
			default:
				if _, ok := err.(recipebuilder.Error); ok {
					statusCode = http.StatusBadRequest
				} else {
					statusCode = http.StatusServiceUnavailable
				}
			}
		} else {
			statusCode = http.StatusAccepted
			desiredLRPCounter.Increment()
		}
	}

	resp.WriteHeader(statusCode)
}

func (h *DesireAppHandler) getDesiredRC(logger lager.Logger, processGuid string) (*api.ReplicationController, error) {
	logger = logger.Session("fetching-desired-rc")
	k8sClient := h.k8sClient
	var err error

	_, err = k8sClient.ServerVersion()
	if err != nil {
		logger.Fatal("Can't connect to Kubernetes API", err)
		return nil, err
	}

	logger.Debug("Connected to Kubernetes API")
	// TODO: add the code to check if the processGuid exists in Kube
	rc, err := k8sClient.ReplicationControllers(namespace).Get(processGuid)
	if err != nil {
		logger.Error("Not able to find the RC", err)
		return nil, err
	}
	return rc, nil
}

func (h *DesireAppHandler) createDesiredApp(logger lager.Logger, desireAppMessage cc_messages.DesireAppRequestFromCC) error {
	logger = logger.Session("creating-desired-lrp")
	newRC, err := transformer.DesiredAppToRC(logger, desireAppMessage)
	if err != nil {
		logger.Fatal("failed-to-transform-desired-app-to-rc", err)
	}

	k8sClient := h.k8sClient

	_, err = k8sClient.ServerVersion()
	if err != nil {
		logger.Fatal("Can't connect to Kubernetes API", err)
		return err
	}

	logger.Debug("Connected to Kubernetes API")
	_, err = k8sClient.ReplicationControllers(namespace).Create(newRC)
	if err != nil {
		logger.Fatal("failed-to-create-lrp", err)
		return err
	}

	logger.Debug("created-desired-lrp")

	return nil
}

func (h *DesireAppHandler) updateDesiredApp(
	logger lager.Logger,
	existingRC *api.ReplicationController,
	desireAppMessage cc_messages.DesireAppRequestFromCC,
) error {
	k8sClient := h.k8sClient
	var err error

	_, err = k8sClient.ServerVersion()
	if err != nil {
		logger.Fatal("Can't connect to Kubernetes API", err)
		return err
	}

	logger.Debug("Connected to Kubernetes API %s")

	logger.Debug("updating-desired-lrp")
	newRC, err := transformer.DesiredAppToRC(logger, desireAppMessage)
	_, err = k8sClient.ReplicationControllers(namespace).Update(newRC)

	if err != nil {
		logger.Fatal("failed-to-update-lrp", err)
		return err
	}

	logger.Debug("updated-desired-lrp")

	return nil

	/*var builder recipebuilder.RecipeBuilder = h.recipeBuilders["buildpack"]
	if desireAppMessage.DockerImageUrl != "" {
		builder = h.recipeBuilders["docker"]
	}
	ports, err := builder.ExtractExposedPorts(&desireAppMessage)
	if err != nil {
		logger.Error("failed to-get-exposed-port", err)
		return err
	}

	updateRoutes, err := helpers.CCRouteInfoToRoutes(desireAppMessage.RoutingInfo, ports)
	if err != nil {
		logger.Error("failed-to-marshal-routes", err)
		return err
	}

	routes := existingLRP.Routes
	if routes == nil {
		routes = &models.Routes{}
	}

	if value, ok := updateRoutes[cfroutes.CF_ROUTER]; ok {
		(*routes)[cfroutes.CF_ROUTER] = value
	}
	if value, ok := updateRoutes[tcp_routes.TCP_ROUTER]; ok {
		(*routes)[tcp_routes.TCP_ROUTER] = value
	}
	instances := int32(desireAppMessage.NumInstances)
	updateRequest := &models.DesiredLRPUpdate{
		Annotation: &desireAppMessage.ETag,
		Instances:  &instances,
		Routes:     routes,
	}

	logger.Debug("updating-desired-lrp", lager.Data{"routes": sanitizeRoutes(existingLRP.Routes)})
	err = h.bbsClient.UpdateDesiredLRP(logger, desireAppMessage.ProcessGuid, updateRequest)
	if err != nil {
		logger.Error("failed-to-update-lrp", err)
		return err
	}*/

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
