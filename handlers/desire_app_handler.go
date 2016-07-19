package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/cloudfoundry-incubator/bbs/models"
	"github.com/cloudfoundry-incubator/nsync/handlers/transformer"
	"github.com/cloudfoundry-incubator/nsync/recipebuilder"
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
)

type DesireAppHandler struct {
	recipeBuilders map[string]recipebuilder.RecipeBuilder
	logger         lager.Logger
	k8sClient      unversioned.Interface
}

func NewDesireAppHandler(logger lager.Logger, builders map[string]recipebuilder.RecipeBuilder, k8sClient unversioned.Interface) DesireAppHandler {
	return DesireAppHandler{
		recipeBuilders: builders,
		logger:         logger,
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

	logger.Info("request-from-cc", lager.Data{"routing_info": desiredApp.RoutingInfo})

	envNames := []string{}
	var vcapApp string
	var rcGUID string
	var ok bool
	var spaceID string

	for _, envVar := range desiredApp.Environment {
		envNames = append(envNames, envVar.Name)
		if envVar.Name == "VCAP_APPLICATION" {
			vcapApp = envVar.Value
		}
	}
	/*sample data
	{"keys":["VCAP_APPLICATION","MEMORY_LIMIT","VCAP_SERVICES"],"method":"PUT","process_guid":"e9640a75-9ddf-4351-bccd-21264640c156-4291ad33-41be-4675-8fc5-cfb72af8047b","request":"/v1/apps/e9640a75-9ddf-4351-bccd-21264640c156-4291ad33-41be-4675-8fc5-cfb72af8047b?%3Aprocess_guid=e9640a75-9ddf-4351-bccd-21264640c156-4291ad33-41be-4675-8fc5-cfb72af8047b\u0026","session":"2"}}
	*/
	logger.Debug("environment", lager.Data{"environment": envNames})
	logger.Debug("environment keys", lager.Data{"keys": desiredApp.Environment})
	logger.Debug("environment vcap_application", lager.Data{"VCAP_APPLICATION": vcapApp})

	m := map[string]string{}
	errUnmarshal := json.Unmarshal([]byte(vcapApp), &m)
	if errUnmarshal != nil {
		logger.Error("failed to unmarshal vcapApp", errUnmarshal, lager.Data{"VCAP_APPLICATION": vcapApp})
	}
	//spaceID, ok = m["space_id"] TODO: enable this when we fignure out how to get space_id during stop cmd
	spaceID, ok = m["application_id"]
	if ok == false {
		logger.Error("unable to find space_id in environment vcap_application", errors.New("unable to find space_id"))
	}

	// kube requires replication controller name < 63
	if len(desiredApp.ProcessGuid) >= 63 {
		rcGUID = desiredApp.ProcessGuid[:60]
	} else {
		rcGUID = desiredApp.ProcessGuid
	}

	if processGuid != desiredApp.ProcessGuid {
		logger.Error("process-guid-mismatch", err, lager.Data{"body-process-guid": desiredApp.ProcessGuid})
		resp.WriteHeader(http.StatusBadRequest)
		return
	}

	statusCode := http.StatusConflict
	for tries := 2; tries > 0 && statusCode == http.StatusConflict; tries-- {
		existingRC, err := h.getDesiredRC(logger, rcGUID, spaceID)

		if err == nil && existingRC != nil {
			err = h.updateDesiredApp(logger, existingRC, desiredApp, spaceID)
		} else if err != nil && err.Error() == "replicationcontrollers \""+rcGUID+"\" not found" {
			err = h.createDesiredApp(logger, desiredApp, spaceID)
		} else {
			logger.Error("Unable to create or update desired app ", err)
			statusCode = http.StatusServiceUnavailable
			break
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

	logger.Debug("statusCode", lager.Data{"statusCode": statusCode})
	resp.WriteHeader(statusCode)
}

func (h *DesireAppHandler) getDesiredRC(logger lager.Logger, processGuid string, namespace string) (*api.ReplicationController, error) {
	logger = logger.Session("fetching-desired-rc")
	k8sClient := h.k8sClient
	var err error

	_, err = k8sClient.Namespaces().Get(namespace)

	if err != nil && err.Error() == "namespaces \""+namespace+"\" not found" {
		apiNS := &api.Namespace{
			ObjectMeta: api.ObjectMeta{
				Name: namespace,
			},
			Spec: api.NamespaceSpec{
				Finalizers: []api.FinalizerName{},
			},
		}
		_, err = k8sClient.Namespaces().Create(apiNS)
		if err != nil {
			logger.Error("Not able to create namespace", err, lager.Data{"namespace": namespace})
		}
	}

	rc, err := k8sClient.ReplicationControllers(namespace).Get(processGuid)

	return rc, err
}

func (h *DesireAppHandler) createDesiredApp(logger lager.Logger, desireAppMessage cc_messages.DesireAppRequestFromCC, namespace string) error {
	logger = logger.Session("creating-desired-lrp")
	newRC, err := transformer.DesiredAppToRC(logger, desireAppMessage)
	if err != nil {
		logger.Fatal("failed-to-transform-desired-app-to-rc", err)
	}

	k8sClient := h.k8sClient

	_, err = k8sClient.ReplicationControllers(namespace).Create(newRC)
	if err != nil {
		logger.Error("failed-to-create-lrp", err)
		return err
	}
	logger.Debug("created-desired-lrp")

	return nil
}

func (h *DesireAppHandler) updateDesiredApp(
	logger lager.Logger,
	existingRC *api.ReplicationController,
	desireAppMessage cc_messages.DesireAppRequestFromCC,
	namespace string,
) error {
	k8sClient := h.k8sClient
	var err error

	/*_, err = k8sClient.ServerVersion()
	if err != nil {
		logger.Fatal("Can't connect to Kubernetes API", err)
		return err
	}

	logger.Debug("Connected to Kubernetes API %s")*/

	logger.Debug("updating-desired-lrp")
	newRC, err := transformer.DesiredAppToRC(logger, desireAppMessage)
	_, err = k8sClient.ReplicationControllers(namespace).Update(newRC)

	if err != nil {
		logger.Error("failed-to-update-lrp", err)
		return err
	}

	logger.Debug("updated-desired-lrp")

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
