package bulk

import (
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/api/v1"
	v1core "k8s.io/kubernetes/pkg/client/clientset_generated/release_1_3/typed/core/v1"
	"k8s.io/kubernetes/pkg/labels"

	"github.com/cloudfoundry-incubator/bbs/models"
	"github.com/cloudfoundry-incubator/cf_http"
	"github.com/cloudfoundry-incubator/nsync/helpers"
	"github.com/cloudfoundry-incubator/nsync/recipebuilder"
	"github.com/cloudfoundry-incubator/runtime-schema/cc_messages"
	"github.com/cloudfoundry-incubator/runtime-schema/metric"
	"github.com/cloudfoundry/gunk/workpool"
	"github.com/pivotal-golang/clock"
	"github.com/pivotal-golang/lager"
	kubeerrors "k8s.io/kubernetes/pkg/api/errors"
)

const (
	syncDesiredLRPsDuration = metric.Duration("DesiredLRPSyncDuration")
	invalidLRPsFound        = metric.Metric("NsyncInvalidDesiredLRPsFound")
)

type LRPProcessor struct {
	k8sClient             v1core.CoreInterface
	pollingInterval       time.Duration
	domainTTL             time.Duration
	bulkBatchSize         uint
	updateLRPWorkPoolSize int
	httpClient            *http.Client
	logger                lager.Logger
	fetcher               Fetcher
	furnaceBuilders       map[string]recipebuilder.FurnaceRecipeBuilder
	clock                 clock.Clock
}

type CCDesiredAppFingerprintWithShortenedGUID struct {
	ProcessGuid string `json:"process_guid"`
	ETag        string `json:"etag"`
}

type KubeSchedulingInfo struct {
	ProcessGuid string
	Annotation  map[string]string `json:"annotations,omitempty" protobuf:"bytes,12,rep,name=annotations"`
	Instances   *int32            `json:"replicas,omitempty" protobuf:"varint,1,opt,name=replicas"`
	//Routes               models.Routes     `protobuf:"bytes,5,opt,name=routes,customtype=Routes" json:"routes"`
	ETag string
}

func NewLRPProcessor(
	logger lager.Logger,
	k8sClient v1core.CoreInterface,
	pollingInterval time.Duration,
	domainTTL time.Duration,
	bulkBatchSize uint,
	updateLRPWorkPoolSize int,
	skipCertVerify bool,
	fetcher Fetcher,
	furnaceBuilders map[string]recipebuilder.FurnaceRecipeBuilder,
	clock clock.Clock,
) *LRPProcessor {
	return &LRPProcessor{
		k8sClient:             k8sClient,
		pollingInterval:       pollingInterval,
		domainTTL:             domainTTL,
		bulkBatchSize:         bulkBatchSize,
		updateLRPWorkPoolSize: updateLRPWorkPoolSize,
		httpClient:            initializeHttpClient(skipCertVerify),
		logger:                logger,
		fetcher:               fetcher,
		furnaceBuilders:       furnaceBuilders,
		clock:                 clock,
	}
}

func (l *LRPProcessor) Run(signals <-chan os.Signal, ready chan<- struct{}) error {
	close(ready)

	timer := l.clock.NewTimer(l.pollingInterval)
	stop := l.sync(signals)

	for {
		if stop {
			return nil
		}

		select {
		case <-signals:
			return nil
		case <-timer.C():
			stop = l.sync(signals)
			timer.Reset(l.pollingInterval)
		}
	}
}

func (l *LRPProcessor) sync(signals <-chan os.Signal) bool {
	start := l.clock.Now()
	invalidsFound := int32(0)
	logger := l.logger.Session("sync-lrps")
	logger.Info("starting")

	defer func() {
		duration := l.clock.Now().Sub(start)
		err := syncDesiredLRPsDuration.Send(duration)
		if err != nil {
			logger.Error("failed-to-send-sync-desired-lrps-duration-metric", err)
		}
		err = invalidLRPsFound.Send(int(invalidsFound))
		if err != nil {
			logger.Error("failed-to-send-sync-invalid-lrps-found-metric", err)
		}
	}()

	defer logger.Info("done")

	existingSchedulingInfoMap, err := l.getSchedulingInfoMap(logger)
	if err != nil {
		return false
	}

	//existingSchedulingInfoMap := organizeSchedulingInfosByProcessGuid(existing)
	appDiffer := NewAppDiffer(existingSchedulingInfoMap)

	cancelCh := make(chan struct{})

	// from here on out, the fetcher, differ, and processor work across channels in a pipeline
	fingerprintCh, fingerprintErrorCh := l.fetcher.FetchFingerprints(
		logger,
		cancelCh,
		l.httpClient,
	)

	diffErrorCh := appDiffer.Diff(
		logger,
		cancelCh,
		fingerprintCh,
	)

	missingAppCh, missingAppsErrorCh := l.fetcher.FetchDesiredApps(
		logger.Session("fetch-missing-desired-lrps-from-cc"),
		cancelCh,
		l.httpClient,
		appDiffer.Missing(),
	)

	createErrorCh := l.createMissingDesiredLRPs(logger, cancelCh, missingAppCh, &invalidsFound)

	staleAppCh, staleAppErrorCh := l.fetcher.FetchDesiredApps(
		logger.Session("fetch-stale-desired-lrps-from-cc"),
		cancelCh,
		l.httpClient,
		appDiffer.Stale(),
	)

	updateErrorCh := l.updateStaleDesiredLRPs(logger, cancelCh, staleAppCh, existingSchedulingInfoMap, &invalidsFound)

	bumpFreshness := true
	success := true

	fingerprintErrorCh, fingerprintErrorCount := countErrors(fingerprintErrorCh)

	// closes errors when all error channels have been closed.
	// below, we rely on this behavior to break the process_loop.
	logger.Debug("merging all errors")
	errors := mergeErrors(
		fingerprintErrorCh,
		diffErrorCh,
		missingAppsErrorCh,
		staleAppErrorCh,
		createErrorCh,
		updateErrorCh,
	)

	logger.Info("processing-updates-and-creates")
process_loop:
	for {
		select {
		case err, open := <-errors:
			if err != nil {
				logger.Error("not-bumping-freshness-because-of", err)
				bumpFreshness = false
			}
			if !open {
				break process_loop
			}
		case sig := <-signals:
			logger.Info("exiting", lager.Data{"received-signal": sig})
			close(cancelCh)
			return true
		}
	}
	logger.Info("done-processing-updates-and-creates")

	if <-fingerprintErrorCount != 0 {
		logger.Error("failed-to-fetch-all-cc-fingerprints", nil)
		success = false
	}

	if success {
		deleteList := <-appDiffer.Deleted()
		l.deleteExcess(logger, cancelCh, deleteList)
	}

	if bumpFreshness && success {
		logger.Info("bumping-freshness")
	}
	// 	err = l.bbsClient.UpsertDomain(logger, cc_messages.AppLRPDomain, l.domainTTL)
	// 	if err != nil {
	// 		logger.Error("failed-to-upsert-domain", err)
	// 	}
	// }

	return false
}

func (l *LRPProcessor) createMissingDesiredLRPs(
	logger lager.Logger,
	cancel <-chan struct{},
	missing <-chan []cc_messages.DesireAppRequestFromCC,
	invalidCount *int32,
) <-chan error {
	logger = logger.Session("create-missing-desired-lrps")

	errc := make(chan error, 1)

	go func() {
		defer close(errc)

		for {
			var desireAppRequests []cc_messages.DesireAppRequestFromCC

			select {
			case <-cancel:
				return

			case selected, open := <-missing:
				if !open {
					return
				}

				desireAppRequests = selected
			}

			works := make([]func(), len(desireAppRequests))

			for i, desireAppRequest := range desireAppRequests {
				desireAppRequest := desireAppRequest

				builder := l.furnaceBuilders["buildpack"]
				if desireAppRequest.DockerImageUrl != "" {
					builder = l.furnaceBuilders["docker"]
				}

				works[i] = func() {
					logger.Debug("building-create-desired-lrp-request", desireAppRequestDebugData(&desireAppRequest))
					desired, err := builder.BuildReplicationController(&desireAppRequest)
					if err != nil {
						logger.Error("failed-building-create-desired-lrp-request", err, lager.Data{"process-guid": desireAppRequest.ProcessGuid})
						errc <- err
						return
					}

					logger.Debug("succeeded-building-create-desired-lrp-request", desireAppRequestDebugData(&desireAppRequest))

					logger.Debug("creating-desired-lrp", createDesiredReqDebugData(desired))
					// obtain the namespace from the desired metadata
					namespace := desired.ObjectMeta.Namespace
					_, err = l.k8sClient.ReplicationControllers(namespace).Create(desired)
					if err != nil {
						logger.Error("failed-creating-desired-lrp", err, lager.Data{"process-guid": desireAppRequest.ProcessGuid})
						if models.ConvertError(err).Type == models.Error_InvalidRequest {
							atomic.AddInt32(invalidCount, int32(1))
						} else {
							errc <- err
						}
						return
					}
					logger.Debug("succeeded-creating-desired-lrp", createDesiredReqDebugData(desired))
				}
			}

			throttler, err := workpool.NewThrottler(l.updateLRPWorkPoolSize, works)
			if err != nil {
				errc <- err
				return
			}

			logger.Info("processing-batch", lager.Data{"size": len(desireAppRequests)})
			throttler.Work()
			logger.Info("done-processing-batch", lager.Data{"size": len(desireAppRequests)})
		}
	}()

	return errc
}

func (l *LRPProcessor) updateStaleDesiredLRPs(
	logger lager.Logger,
	cancel <-chan struct{},
	stale <-chan []cc_messages.DesireAppRequestFromCC,
	existingSchedulingInfoMap map[string]*KubeSchedulingInfo,
	invalidCount *int32,
) <-chan error {
	logger = logger.Session("update-stale-desired-lrps")

	errc := make(chan error, 1)

	go func() {
		defer close(errc)

		for {
			var staleAppRequests []cc_messages.DesireAppRequestFromCC

			select {
			case <-cancel:
				return

			case selected, open := <-stale:
				if !open {
					return
				}

				staleAppRequests = selected
			}

			works := make([]func(), len(staleAppRequests))

			for i, desireAppRequest := range staleAppRequests {
				desireAppRequest := desireAppRequest
				builder := l.furnaceBuilders["buildpack"]
				if desireAppRequest.DockerImageUrl != "" {
					builder = l.furnaceBuilders["docker"]
				}

				if builder == nil {
					err := errors.New("oh no builder nil")
					logger.Error("builder cannot be nil", err, lager.Data{"builder": builder})
					errc <- err
					return
				}

				works[i] = func() {
					processGuid, err := helpers.NewProcessGuid(desireAppRequest.ProcessGuid)
					if err != nil {
						logger.Error("failed to create new process guid", err)
						errc <- err
						return
					}
					//existingSchedulingInfo := existingSchedulingInfoMap[processGuid.ShortenedGuid()]

					//instances := int32(desireAppRequest.NumInstances)
					logger.Debug("printing desired app req", lager.Data{"data": desireAppRequest})

					replicationController, err := builder.BuildReplicationController(&desireAppRequest)
					if err != nil {
						logger.Error("failed-to-transform-stale-app-to-rc", err)
						errc <- err
						return
					}
					// exposedPorts, err := builder.ExtractExposedPorts(&desireAppRequest)
					// if err != nil {
					// 	logger.Error("failed-updating-stale-lrp", err, lager.Data{
					// 		"process-guid":       processGuid,
					// 		"execution-metadata": desireAppRequest.ExecutionMetadata,
					// 	})
					// 	errc <- err
					// 	return
					// }
					//
					// routes, err := helpers.CCRouteInfoToRoutes(desireAppRequest.RoutingInfo, exposedPorts)
					// if err != nil {
					// 	logger.Error("failed-to-marshal-routes", err)
					// 	errc <- err
					// 	return
					// }
					//
					// updateReq.Routes = &routes
					//
					// for k, v := range existingSchedulingInfo.Routes {
					// 	if k != cfroutes.CF_ROUTER && k != tcp_routes.TCP_ROUTER {
					// 		(*updateReq.Routes)[k] = v
					// 	}
					// }

					logger.Debug("updating-stale-lrp", updateDesiredRequestDebugData(processGuid.String(), replicationController))

					_, err1 := l.k8sClient.ReplicationControllers(replicationController.ObjectMeta.Namespace).Update(replicationController)
					if err1 != nil {
						logger.Error("failed-updating-stale-lrp", err1, lager.Data{
							"process-guid": processGuid,
						})

						errc <- err1
						return
					}
					logger.Debug("succeeded-updating-stale-lrp", updateDesiredRequestDebugData(processGuid.String(), replicationController))
				}
			}

			throttler, err := workpool.NewThrottler(l.updateLRPWorkPoolSize, works)
			if err != nil {
				errc <- err
				return
			}

			logger.Info("processing-batch", lager.Data{"size": len(staleAppRequests)})
			throttler.Work()
			logger.Info("done-processing-batch", lager.Data{"size": len(staleAppRequests)})
		}
	}()

	return errc
}

func (l *LRPProcessor) getSchedulingInfoMap(logger lager.Logger) (map[string]*KubeSchedulingInfo, error) {
	logger.Info("getting-desired-lrps-from-kube")
	opts := api.ListOptions{
		LabelSelector: labels.Set{"cloudfoundry.org/domain": cc_messages.AppLRPDomain}.AsSelector(),
	}
	//logger.Debug("opts to list replication controller", lager.Data{"data": opts.LabelSelector.String()})
	rcList, err := l.k8sClient.ReplicationControllers(api.NamespaceAll).List(opts)
	if err != nil {
		logger.Error("failed-getting-desired-lrps-from-kube", err)
		return nil, err
	}
	logger.Debug("list replication controller", lager.Data{"data": rcList.Items})

	existing := make(map[string]*KubeSchedulingInfo)
	if len(rcList.Items) == 0 {
		logger.Info("empty kube scheduling info found")
		return existing, nil
	}
	for _, rc := range rcList.Items {
		shortenedProcessGuid := rc.ObjectMeta.Name // shortened guid, todo: need to convert to original guids
		existing[shortenedProcessGuid] = &KubeSchedulingInfo{
			ProcessGuid: shortenedProcessGuid,
			Annotation:  rc.ObjectMeta.Annotations,
			Instances:   rc.Spec.Replicas,
			ETag:        rc.ObjectMeta.Annotations["cloudfoundry.org/etag"],
		}
	}

	logger.Info("succeeded-getting-desired-lrps-from-kube", lager.Data{"count": len(existing)})

	return existing, nil
}

func (l *LRPProcessor) deleteExcess(logger lager.Logger, cancel <-chan struct{}, excess []string) {
	logger = logger.Session("delete-excess")

	logger.Info("processing-batch", lager.Data{"num-to-delete": len(excess), "guids-to-delete": excess})
	deletedGuids := make([]string, 0, len(excess))
	for _, deleteGuid := range excess {
		_, err := deleteReplicationController(logger, l.k8sClient, deleteGuid)
		if err != nil {
			logger.Error("failed-processing-batch", err, lager.Data{"delete-request": deleteGuid})
		} else {
			deletedGuids = append(deletedGuids, deleteGuid)
		}
	}
	logger.Info("succeeded-processing-batch", lager.Data{"num-deleted": len(deletedGuids), "deleted-guids": deletedGuids})
}

func countErrors(source <-chan error) (<-chan error, <-chan int) {
	count := make(chan int, 1)
	dest := make(chan error, 1)
	var errorCount int

	wg := sync.WaitGroup{}
	wg.Add(1)

	go func() {
		for e := range source {
			errorCount++
			dest <- e
		}

		close(dest)
		wg.Done()
	}()

	go func() {
		wg.Wait()

		count <- errorCount
		close(count)
	}()

	return dest, count
}

func mergeErrors(channels ...<-chan error) <-chan error {
	out := make(chan error)
	wg := sync.WaitGroup{}

	for _, ch := range channels {
		wg.Add(1)

		go func(c <-chan error) {
			for e := range c {
				out <- e
			}
			wg.Done()
		}(ch)
	}

	go func() {
		wg.Wait()
		close(out)
	}()

	return out
}

func organizeSchedulingInfosByProcessGuid(list []*models.DesiredLRPSchedulingInfo) map[string]*models.DesiredLRPSchedulingInfo {
	result := make(map[string]*models.DesiredLRPSchedulingInfo)
	for _, l := range list {
		lrp := l
		result[lrp.ProcessGuid] = lrp
	}

	return result
}

func updateDesiredRequestDebugData(processGuid string, rc *v1.ReplicationController) lager.Data {
	return lager.Data{
		"process-guid": processGuid,
		"instances":    rc.Spec.Replicas,
		"etag":         rc.ObjectMeta.Annotations["cloudfoundry.org/etag"],
	}
}

func createDesiredReqDebugData(rc *v1.ReplicationController) lager.Data {
	container := rc.Spec.Template.Spec.Containers[0]
	return lager.Data{
		"process-guid": rc.ObjectMeta.Labels["cloudfoundry.org/process-guid"],
		"log-guid":     rc.ObjectMeta.Annotations["cloudfoundry.org/log-guid"],
		"metric-guid":  rc.ObjectMeta.Annotations["cloudfoundry.org/metrics-guid"],
		"instances":    rc.Spec.Replicas,
		//"timeout":      createDesiredRequest.StartTimeoutMs,
		"disk":   container.Resources.Limits["cloudfoundry.org/storage-space"],
		"memory": container.Resources.Limits[v1.ResourceMemory],
		"cpu":    container.Resources.Limits[v1.ResourceCPU],
		//"privileged":   createDesiredRequest.Privileged,
	}
}

func desireAppRequestDebugData(desireAppRequest *cc_messages.DesireAppRequestFromCC) lager.Data {
	return lager.Data{
		"process-guid": desireAppRequest.ProcessGuid,
		"log-guid":     desireAppRequest.LogGuid,
		"stack":        desireAppRequest.Stack,
		"memory":       desireAppRequest.MemoryMB,
		"disk":         desireAppRequest.DiskMB,
		"file":         desireAppRequest.FileDescriptors,
		"instances":    desireAppRequest.NumInstances,
		"allow-ssh":    desireAppRequest.AllowSSH,
		"etag":         desireAppRequest.ETag,
	}
}

func initializeHttpClient(skipCertVerify bool) *http.Client {
	httpClient := cf_http.NewClient()
	httpClient.Transport = &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		Dial: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).Dial,
		TLSHandshakeTimeout: 10 * time.Second,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: skipCertVerify,
			MinVersion:         tls.VersionTLS10,
		},
	}
	return httpClient
}

func deleteReplicationController(logger lager.Logger, k8sClient v1core.CoreInterface, processGuid string) (int, error) {
	rcList, err := k8sClient.ReplicationControllers(api.NamespaceAll).List(api.ListOptions{
		LabelSelector: labels.Set{"cloudfoundry.org/process-guid": processGuid}.AsSelector(),
	})

	if err != nil {
		logger.Error("replication-controller-list-failed", err)
		return http.StatusInternalServerError, err
	}

	if len(rcList.Items) == 0 {
		logger.Info("desired-lrp-not-found")
		return http.StatusNotFound, err
	}

	logger.Debug("deleting replication controllers", lager.Data{"to be deleted": rcList})
	for _, rcValue := range rcList.Items {
		rc := &rcValue
		rc.Spec.Replicas = helpers.Int32Ptr(0)

		rc, err = k8sClient.ReplicationControllers(rc.ObjectMeta.Namespace).Update(rc)
		if err != nil {
			logger.Error("update-replication-controller-failed", err)
			code := responseCodeFromError(err)
			if code == http.StatusNotFound {
				continue
			}
			return code, err
		}

		if err := k8sClient.ReplicationControllers(rc.ObjectMeta.Namespace).Delete(rc.ObjectMeta.Name, nil); err != nil {
			logger.Error("delete-replication-controller-failed", err)
			code := responseCodeFromError(err)
			if code == http.StatusNotFound {
				continue
			}

			return code, err
		} else {
			logger.Debug("deleted replication controller successfully", lager.Data{"rc": rc.ObjectMeta.Name})
		}
	}

	return http.StatusAccepted, nil
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
