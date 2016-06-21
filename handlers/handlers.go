package handlers

import (
	"net/http"

	"k8s.io/kubernetes/pkg/client/unversioned"

	"github.com/cf-furnace/nsync"
	"github.com/cf-furnace/nsync/recipebuilder"
	"github.com/cloudfoundry-incubator/bbs"
	"github.com/pivotal-golang/lager"
	"github.com/tedsuo/rata"
)

func New(logger lager.Logger, bbsClient bbs.Client, recipebuilders map[string]recipebuilder.RecipeBuilder, k8sClient *unversioned.Client) http.Handler {
	desireAppHandler := NewDesireAppHandler(logger, recipebuilders, k8sClient)
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
