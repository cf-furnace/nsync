package handlers

import (
	"net/http"

	"github.com/cloudfoundry-incubator/bbs"
	"github.com/cloudfoundry-incubator/nsync"
	"github.com/cloudfoundry-incubator/nsync/recipebuilder"
	"github.com/pivotal-golang/lager"
	"github.com/tedsuo/rata"
	v1core "k8s.io/kubernetes/pkg/client/clientset_generated/release_1_3/typed/core/v1"
)

func New(
	logger lager.Logger,
	bbsClient bbs.Client,
	recipebuilders map[string]recipebuilder.RecipeBuilder,
	furnaceBuilders map[string]recipebuilder.FurnaceRecipeBuilder,
	k8sClient v1core.CoreInterface,
) http.Handler {
	desireAppHandler := NewDesireAppHandler(logger, furnaceBuilders, k8sClient)
	stopAppHandler := NewStopAppHandler(logger, k8sClient)
	killIndexHandler := NewKillIndexHandler(logger, bbsClient)
	taskHandler := NewTaskHandler(logger, bbsClient, recipebuilders)
	cancelTaskHandler := NewCancelTaskHandler(logger, bbsClient)

	actions := rata.Handlers{
		nsync.DesireAppRoute:  http.HandlerFunc(desireAppHandler.DesireApp),
		nsync.StopAppRoute:    http.HandlerFunc(stopAppHandler.StopApp),
		nsync.KillIndexRoute:  http.HandlerFunc(killIndexHandler.KillIndex),
		nsync.TasksRoute:      http.HandlerFunc(taskHandler.DesireTask),
		nsync.CancelTaskRoute: http.HandlerFunc(cancelTaskHandler.CancelTask),
	}

	handler, err := rata.NewRouter(nsync.Routes, actions)
	if err != nil {
		panic("unable to create router: " + err.Error())
	}

	return handler
}
