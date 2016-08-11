package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	clientset "k8s.io/kubernetes/pkg/client/clientset_generated/release_1_3"
	"k8s.io/kubernetes/pkg/client/restclient"

	"github.com/cloudfoundry-incubator/cf-debug-server"
	cf_lager "github.com/cloudfoundry-incubator/cf-lager"
	"github.com/cloudfoundry-incubator/cf_http"
	"github.com/cloudfoundry-incubator/consuladapter"
	"github.com/cloudfoundry-incubator/diego-ssh/keys"
	"github.com/cloudfoundry-incubator/locket"
	"github.com/cloudfoundry-incubator/runtime-schema/cc_messages/flags"
	"github.com/cloudfoundry/dropsonde"
	"github.com/nu7hatch/gouuid"
	"github.com/pivotal-golang/clock"
	"github.com/pivotal-golang/lager"
	"github.com/tedsuo/ifrit"
	"github.com/tedsuo/ifrit/grouper"
	"github.com/tedsuo/ifrit/sigmon"

	"github.com/cloudfoundry-incubator/nsync"
	"github.com/cloudfoundry-incubator/nsync/bulk"
	"github.com/cloudfoundry-incubator/nsync/recipebuilder"
)

var privilegedContainers = flag.Bool(
	"privilegedContainers",
	false,
	"Whether or not to use privileged containers for buildpack based LRPs and tasks. Containers with a docker-image-based rootfs will continue to always be unprivileged and cannot be changed.",
)

var consulCluster = flag.String(
	"consulCluster",
	"",
	"comma-separated list of consul server URLs (scheme://ip:port)",
)

var lockTTL = flag.Duration(
	"lockTTL",
	locket.LockTTL,
	"TTL for service lock",
)

var lockRetryInterval = flag.Duration(
	"lockRetryInterval",
	locket.RetryInterval,
	"interval to wait before retrying a failed lock acquisition",
)

var dropsondePort = flag.Int(
	"dropsondePort",
	3457,
	"port the local metron agent is listening on",
)

var ccBaseURL = flag.String(
	"ccBaseURL",
	"",
	"base URL of the cloud controller",
)

var ccUsername = flag.String(
	"ccUsername",
	"",
	"basic auth username for CC bulk API",
)

var ccPassword = flag.String(
	"ccPassword",
	"",
	"basic auth password for CC bulk API",
)

var communicationTimeout = flag.Duration(
	"communicationTimeout",
	30*time.Second,
	"Timeout applied to all HTTP requests.",
)

var pollingInterval = flag.Duration(
	"pollingInterval",
	30*time.Second,
	"interval at which to poll bulk API",
)

var domainTTL = flag.Duration(
	"domainTTL",
	2*time.Minute,
	"duration of the domain; bumped on every bulk sync",
)

var bulkBatchSize = flag.Uint(
	"bulkBatchSize",
	500,
	"number of apps to fetch at once from bulk API",
)

var skipCertVerify = flag.Bool(
	"skipCertVerify",
	false,
	"skip SSL certificate verification",
)

var fileServerURL = flag.String(
	"fileServerURL",
	"",
	"URL of the file server",
)

var updateLRPWorkers = flag.Int(
	"updateLRPWorkers",
	50,
	"Max concurrency for updating/creating lrps",
)

var failTaskPoolSize = flag.Int(
	"failTaskPoolSize",
	50,
	"Max concurrency for failing mismatched tasks",
)

var cancelTaskPoolSize = flag.Int(
	"cancelTaskPoolSize",
	50,
	"Max concurrency for canceling mismatched tasks",
)

const (
	dropsondeOrigin = "nsync_bulker"
)

var kubeCluster = flag.String(
	"kubeCluster",
	"",
	"kubernetes API server URL (scheme://ip:port)",
)

var kubeCACert = flag.String(
	"kubeCACert",
	"",
	"path to kubernetes API server CA certificate",
)

var kubeClientCert = flag.String(
	"kubeClientCert",
	"",
	"path to client certificate for authentication with the kubernetes API server",
)

var kubeClientKey = flag.String(
	"kubeClientKey",
	"",
	"path to client key for authentication with the kubernetes API server",
)

func main() {
	cf_debug_server.AddFlags(flag.CommandLine)
	cf_lager.AddFlags(flag.CommandLine)

	lifecycles := flags.LifecycleMap{}
	flag.Var(&lifecycles, "lifecycle", "app lifecycle binary bundle mapping (lifecycle[/stack]:bundle-filepath-in-fileserver)")
	flag.Parse()

	cf_http.Initialize(*communicationTimeout)

	logger, reconfigurableSink := cf_lager.New("nsync-bulker")
	initializeDropsonde(logger)

	serviceClient := initializeServiceClient(logger)
	uuid, err := uuid.NewV4()
	if err != nil {
		logger.Fatal("Couldn't generate uuid", err)
	}
	lockMaintainer := serviceClient.NewNsyncBulkerLockRunner(logger, uuid.String(), *lockRetryInterval, *lockTTL)

	dockerRecipeBuilderConfig := recipebuilder.Config{
		Lifecycles:    lifecycles,
		FileServerURL: *fileServerURL,
		KeyFactory:    keys.RSAKeyPairFactory,
	}

	buildpackRecipeBuilderConfig := recipebuilder.Config{
		Lifecycles:           lifecycles,
		FileServerURL:        *fileServerURL,
		KeyFactory:           keys.RSAKeyPairFactory,
		PrivilegedContainers: *privilegedContainers,
	}

	furnaceBuilders := map[string]recipebuilder.FurnaceRecipeBuilder{
		"buildpack": recipebuilder.NewBuildpackRecipeBuilder(logger, buildpackRecipeBuilderConfig),
		"docker":    recipebuilder.NewDockerRecipeBuilder(logger, dockerRecipeBuilderConfig),
	}

	clientSet := initializeK8sClient(logger)

	lrpRunner := bulk.NewLRPProcessor(
		logger,
		clientSet.Core(),
		*pollingInterval,
		*domainTTL,
		*bulkBatchSize,
		*updateLRPWorkers,
		*skipCertVerify,
		&bulk.CCFetcher{
			BaseURI:   *ccBaseURL,
			BatchSize: int(*bulkBatchSize),
			Username:  *ccUsername,
			Password:  *ccPassword,
		},
		furnaceBuilders,
		clock.NewClock(),
	)

	members := grouper.Members{
		{"lock-maintainer", lockMaintainer},
		{"lrp-runner", lrpRunner},
	}

	if dbgAddr := cf_debug_server.DebugAddress(flag.CommandLine); dbgAddr != "" {
		members = append(grouper.Members{
			{"debug-server", cf_debug_server.Runner(dbgAddr, reconfigurableSink)},
		}, members...)
	}

	group := grouper.NewOrdered(os.Interrupt, members)

	logger.Info("waiting-for-lock")

	monitor := ifrit.Invoke(sigmon.New(group))

	logger.Info("started")

	err = <-monitor.Wait()
	if err != nil {
		logger.Error("exited-with-failure", err)
		os.Exit(1)
	}

	logger.Info("exited")
	os.Exit(0)
}

func initializeDropsonde(logger lager.Logger) {
	dropsondeDestination := fmt.Sprint("127.0.0.1:", *dropsondePort)
	err := dropsonde.Initialize(dropsondeDestination, dropsondeOrigin)
	if err != nil {
		logger.Error("failed to initialize dropsonde: %v", err)
	}
}

func initializeServiceClient(logger lager.Logger) nsync.ServiceClient {
	consulClient, err := consuladapter.NewClientFromUrl(*consulCluster)
	if err != nil {
		logger.Fatal("new-client-failed", err)
	}

	return nsync.NewServiceClient(consulClient, clock.NewClock())
}

func initializeK8sClient(logger lager.Logger) clientset.Interface {
	k8sClient, err := clientset.NewForConfig(&restclient.Config{
		Host: *kubeCluster,
		TLSClientConfig: restclient.TLSClientConfig{
			CertFile: *kubeClientCert,
			KeyFile:  *kubeClientKey,
			CAFile:   *kubeCACert,
		},
	})

	if err != nil {
		logger.Fatal("Can't create Kubernetes Client", err, lager.Data{"address": *kubeCluster})
	}

	return k8sClient
}
