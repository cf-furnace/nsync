package recipebuilder

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"k8s.io/kubernetes/pkg/api/resource"
	"k8s.io/kubernetes/pkg/api/v1"

	"github.com/cloudfoundry-incubator/bbs/models"
	ssh_routes "github.com/cloudfoundry-incubator/diego-ssh/routes"
	"github.com/cloudfoundry-incubator/nsync/helpers"
	"github.com/cloudfoundry-incubator/runtime-schema/cc_messages"
	"github.com/pivotal-golang/lager"
)

type BuildpackRecipeBuilder struct {
	logger lager.Logger
	config Config
}

func NewBuildpackRecipeBuilder(logger lager.Logger, config Config) *BuildpackRecipeBuilder {
	return &BuildpackRecipeBuilder{
		logger: logger,
		config: config,
	}
}

func (b *BuildpackRecipeBuilder) BuildTask(task *cc_messages.TaskRequestFromCC) (*models.TaskDefinition, error) {
	logger := b.logger.Session("build-task", lager.Data{"request": task})

	if task.DropletUri == "" {
		logger.Error("missing-droplet-source", ErrDropletSourceMissing)
		return nil, ErrDropletSourceMissing
	}

	if task.DockerPath != "" {
		logger.Error("invalid-docker-path", ErrMultipleAppSources)
		return nil, ErrMultipleAppSources
	}

	downloadAction := &models.DownloadAction{
		From:     task.DropletUri,
		To:       ".",
		CacheKey: "",
		User:     "vcap",
	}

	if task.DropletHash != "" {
		downloadAction.ChecksumAlgorithm = "sha1"
		downloadAction.ChecksumValue = task.DropletHash
	}

	logger.Info("downloadAction", lager.Data{"action": downloadAction})

	runAction := &models.RunAction{
		User:           "vcap",
		Path:           "/tmp/lifecycle/launcher",
		Args:           []string{"app", task.Command, ""},
		Env:            task.EnvironmentVariables,
		LogSource:      task.LogSource,
		ResourceLimits: &models.ResourceLimits{},
	}

	var lifecycle = "buildpack/" + task.RootFs
	lifecyclePath, ok := b.config.Lifecycles[lifecycle]
	if !ok {
		logger.Error("unknown-lifecycle", ErrNoLifecycleDefined, lager.Data{
			"lifecycle": lifecycle,
		})

		return nil, ErrNoLifecycleDefined
	}

	lifecycleURL := lifecycleDownloadURL(lifecyclePath, b.config.FileServerURL)

	cachedDependencies := []*models.CachedDependency{
		&models.CachedDependency{
			From:     lifecycleURL,
			To:       "/tmp/lifecycle",
			CacheKey: fmt.Sprintf("%s-lifecycle", strings.Replace(lifecycle, "/", "-", 1)),
		},
	}

	rootFSPath := models.PreloadedRootFS(task.RootFs)

	taskDefinition := &models.TaskDefinition{
		Privileged:            b.config.PrivilegedContainers,
		LogGuid:               task.LogGuid,
		MemoryMb:              int32(task.MemoryMb),
		DiskMb:                int32(task.DiskMb),
		CpuWeight:             cpuWeight(task.MemoryMb),
		EnvironmentVariables:  task.EnvironmentVariables,
		RootFs:                rootFSPath,
		CompletionCallbackUrl: task.CompletionCallbackUrl,
		Action: models.WrapAction(models.Serial(
			downloadAction,
			runAction,
		)),
		CachedDependencies:            cachedDependencies,
		EgressRules:                   task.EgressRules,
		LegacyDownloadUser:            "vcap",
		TrustedSystemCertificatesPath: TrustedSystemCertificatesPath,
		LogSource:                     task.LogSource,
		VolumeMounts:                  task.VolumeMounts,
	}

	return taskDefinition, nil
}

func (b *BuildpackRecipeBuilder) BuildReplicationController(desiredApp *cc_messages.DesireAppRequestFromCC) (*v1.ReplicationController, error) {
	buildLogger := b.logger.Session("buildpack-recipe-build")

	if desiredApp.DropletUri == "" {
		buildLogger.Error("desired-app-invalid", ErrDropletSourceMissing, lager.Data{"desired-app": desiredApp})
		return nil, ErrDropletSourceMissing
	}

	if desiredApp.DropletUri != "" && desiredApp.DockerImageUrl != "" {
		buildLogger.Error("desired-app-invalid", ErrMultipleAppSources, lager.Data{"desired-app": desiredApp})
		return nil, ErrMultipleAppSources
	}

	var lifecycle = "buildpack/" + desiredApp.Stack
	lifecyclePath, ok := b.config.Lifecycles[lifecycle]
	if !ok {
		buildLogger.Error("unknown-lifecycle", ErrNoLifecycleDefined, lager.Data{
			"lifecycle": lifecycle,
		})

		return nil, ErrNoLifecycleDefined
	}

	lifecycleURL := lifecycleDownloadURL(lifecyclePath, b.config.FileServerURL)

	numFiles := DefaultFileDescriptorLimit
	if desiredApp.FileDescriptors != 0 {
		numFiles = desiredApp.FileDescriptors
	}

	desiredAppPorts, err := b.ExtractExposedPorts(desiredApp)
	if err != nil {
		return nil, err
	}

	// switch desiredApp.HealthCheckType {
	// case cc_messages.PortHealthCheckType, cc_messages.UnspecifiedHealthCheckType:
	// 	monitor = models.Timeout(getParallelAction(desiredAppPorts, "vcap"), 30*time.Second)
	// }

	desiredAppRoutingInfo, err := helpers.CCRouteInfoToRoutes(desiredApp.RoutingInfo, desiredAppPorts)
	if err != nil {
		buildLogger.Error("marshaling-cc-route-info-failed", err)
		return nil, err
	}

	// if desiredApp.AllowSSH {
	// 	hostKeyPair, err := b.config.KeyFactory.NewKeyPair(1024)
	// 	if err != nil {
	// 		buildLogger.Error("new-host-key-pair-failed", err)
	// 		return nil, err
	// 	}

	// 	userKeyPair, err := b.config.KeyFactory.NewKeyPair(1024)
	// 	if err != nil {
	// 		buildLogger.Error("new-user-key-pair-failed", err)
	// 		return nil, err
	// 	}

	// 	actions = append(actions, &models.RunAction{
	// 		User: "vcap",
	// 		Path: "/tmp/lifecycle/diego-sshd",
	// 		Args: []string{
	// 			"-address=" + fmt.Sprintf("0.0.0.0:%d", DefaultSSHPort),
	// 			"-hostKey=" + hostKeyPair.PEMEncodedPrivateKey(),
	// 			"-authorizedKey=" + userKeyPair.AuthorizedKey(),
	// 			"-inheritDaemonEnv",
	// 			"-logLevel=fatal",
	// 		},
	// 		Env: createLrpEnv(desiredApp.Environment, desiredAppPorts),
	// 		ResourceLimits: &models.ResourceLimits{
	// 			Nofile: &numFiles,
	// 		},
	// 	})

	// 	sshRoutePayload, err := json.Marshal(ssh_routes.SSHRoute{
	// 		ContainerPort:   2222,
	// 		PrivateKey:      userKeyPair.PEMEncodedPrivateKey(),
	// 		HostFingerprint: hostKeyPair.Fingerprint(),
	// 	})

	// 	if err != nil {
	// 		buildLogger.Error("marshaling-ssh-route-failed", err)
	// 		return nil, err
	// 	}

	// 	sshRouteMessage := json.RawMessage(sshRoutePayload)
	// 	desiredAppRoutingInfo[ssh_routes.DIEGO_SSH] = &sshRouteMessage
	// 	desiredAppPorts = append(desiredAppPorts, DefaultSSHPort)
	// }

	initContainers := []v1.Container{{
		Name:  "setup",
		Image: "cloudfoundry/" + desiredApp.Stack + ":latest",
		VolumeMounts: []v1.VolumeMount{
			{Name: "droplet", MountPath: "/droplet"},
			{Name: "lifecycle", MountPath: "/lifecycle"},
		},
		Env: []v1.EnvVar{
			{Name: "LIFECYCLE_URL", Value: lifecycleURL},
			{Name: "DROPLET_URL", Value: desiredApp.DropletUri},
		},
		SecurityContext: &v1.SecurityContext{
			RunAsUser: helpers.Int64Ptr(2000),
		},
		Command: []string{
			"/bin/bash",
			"-c",
			strings.Join([]string{
				"set -ex",
				"wget --no-check-certificate -O- $LIFECYCLE_URL | tar -zxv -C /lifecycle",
				"wget --no-check-certificate -O- $DROPLET_URL | tar -zxv -C /droplet",
			}, "\n"),
		},
	}}

	processGuid, err := helpers.NewProcessGuid(desiredApp.ProcessGuid)
	if err != nil {
		panic(err)
	}

	spaceGuid, err := GetSpaceGuid(*desiredApp)
	if err != nil {
		panic(err)
	}

	encodedInitContainers, err := json.Marshal(initContainers)
	if err != nil {
		panic(err)
	}

	encodedRoutes, err := json.Marshal(desiredAppRoutingInfo)
	if err != nil {
		panic(err)
	}

	if desiredApp.EgressRules == nil {
		desiredApp.EgressRules = []*models.SecurityGroupRule{}
	}
	encodedEgressRules, err := json.Marshal(desiredApp.EgressRules)
	if err != nil {
		panic(err)
	}

	var langVariableSet bool
	envVars := []v1.EnvVar{}
	for _, e := range createLrpEnv(desiredApp.Environment, desiredAppPorts) {
		if e.Name == "LANG" {
			langVariableSet = true
		}
		envVars = append(envVars, v1.EnvVar{Name: e.Name, Value: e.Value})
	}

	if !langVariableSet {
		envVars = append(envVars, v1.EnvVar{Name: "LANG", Value: DefaultLANG})
	}

	containerPorts := []v1.ContainerPort{}
	for _, p := range desiredAppPorts {
		containerPorts = append(containerPorts, v1.ContainerPort{Protocol: v1.ProtocolTCP, ContainerPort: int32(p)})
	}

	return &v1.ReplicationController{
		ObjectMeta: v1.ObjectMeta{
			Name:      processGuid.ShortenedGuid(),
			Namespace: spaceGuid,
			Labels: map[string]string{
				"cloudfoundry.org/app-guid":     processGuid.AppGuid.String(),
				"cloudfoundry.org/space-guid":   spaceGuid,
				"cloudfoundry.org/process-guid": processGuid.ShortenedGuid(),
			},
			Annotations: map[string]string{
				"cloudfoundry.org/allow-ssh":    strconv.FormatBool(desiredApp.AllowSSH),
				"cloudfoundry.org/routing-info": string(encodedRoutes),
				"cloudfoundry.org/egress-rules": string(encodedEgressRules),
				"cloudfoundry.org/log-source":   getAppLogSource(desiredApp.LogSource),
				"cloudfoundry.org/log-guid":     desiredApp.LogGuid,
				"cloudfoundry.org/metrics-guid": desiredApp.LogGuid,
			},
		},
		Spec: v1.ReplicationControllerSpec{
			Replicas: helpers.Int32Ptr(desiredApp.NumInstances),
			Selector: map[string]string{
				"cloudfoundry.org/process-guid": processGuid.ShortenedGuid(),
			},
			Template: &v1.PodTemplateSpec{
				ObjectMeta: v1.ObjectMeta{
					Labels: map[string]string{
						"cloudfoundry.org/process-guid": processGuid.ShortenedGuid(),
					},
					Annotations: map[string]string{
						v1.PodInitContainersAnnotationKey: string(encodedInitContainers),
					},
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{{
						Name:            "application",
						Image:           "cloudfoundry/" + desiredApp.Stack + ":latest",
						ImagePullPolicy: v1.PullIfNotPresent,
						Resources: v1.ResourceRequirements{
							Limits: v1.ResourceList{
								v1.ResourceCPU:                      resource.MustParse("1000m"),
								v1.ResourceMemory:                   resource.MustParse(fmt.Sprintf("%dMi", desiredApp.MemoryMB)),
								"cloudfoundry.org/storage-space":    resource.MustParse(fmt.Sprintf("%dMi", desiredApp.DiskMB)),
								"cloudfoundry.org/file-descriptors": resource.MustParse(fmt.Sprintf("%d", numFiles)),
							},
						},
						VolumeMounts: []v1.VolumeMount{
							{Name: "droplet", MountPath: "/home/vcap/app"},
							{Name: "lifecycle", MountPath: "/tmp/lifecycle", ReadOnly: true},
						},
						SecurityContext: &v1.SecurityContext{
							RunAsUser: helpers.Int64Ptr(2000),
						},
						Env: envVars,
						Command: []string{
							"/bin/bash",
							"-c",
							`exec /tmp/lifecycle/launcher app "` + desiredApp.StartCommand + `" "` + desiredApp.ExecutionMetadata + `"`,
						},
						WorkingDir: "/home/vcap/app",
						Ports:      containerPorts,
					}},
					Volumes: []v1.Volume{{
						Name: "lifecycle",
						VolumeSource: v1.VolumeSource{
							EmptyDir: &v1.EmptyDirVolumeSource{},
						},
					}, {
						Name: "droplet",
						VolumeSource: v1.VolumeSource{
							EmptyDir: &v1.EmptyDirVolumeSource{},
						},
					}},
				},
			},
		},
	}, nil
}

func (b *BuildpackRecipeBuilder) Build(desiredApp *cc_messages.DesireAppRequestFromCC) (*models.DesiredLRP, error) {
	lrpGuid := desiredApp.ProcessGuid

	buildLogger := b.logger.Session("message-builder")

	if desiredApp.DropletUri == "" {
		buildLogger.Error("desired-app-invalid", ErrDropletSourceMissing, lager.Data{"desired-app": desiredApp})
		return nil, ErrDropletSourceMissing
	}

	if desiredApp.DropletUri != "" && desiredApp.DockerImageUrl != "" {
		buildLogger.Error("desired-app-invalid", ErrMultipleAppSources, lager.Data{"desired-app": desiredApp})
		return nil, ErrMultipleAppSources
	}

	var lifecycle = "buildpack/" + desiredApp.Stack
	lifecyclePath, ok := b.config.Lifecycles[lifecycle]
	if !ok {
		buildLogger.Error("unknown-lifecycle", ErrNoLifecycleDefined, lager.Data{
			"lifecycle": lifecycle,
		})

		return nil, ErrNoLifecycleDefined
	}

	lifecycleURL := lifecycleDownloadURL(lifecyclePath, b.config.FileServerURL)

	rootFSPath := models.PreloadedRootFS(desiredApp.Stack)

	var containerEnvVars []*models.EnvironmentVariable
	containerEnvVars = append(containerEnvVars, &models.EnvironmentVariable{Name: "LANG", Value: DefaultLANG})

	numFiles := DefaultFileDescriptorLimit
	if desiredApp.FileDescriptors != 0 {
		numFiles = desiredApp.FileDescriptors
	}

	var setup []models.ActionInterface
	var actions []models.ActionInterface
	var monitor models.ActionInterface

	cachedDependencies := []*models.CachedDependency{}
	cachedDependencies = append(cachedDependencies, &models.CachedDependency{
		From:     lifecycleURL,
		To:       "/tmp/lifecycle",
		CacheKey: fmt.Sprintf("%s-lifecycle", strings.Replace(lifecycle, "/", "-", 1)),
	})

	desiredAppPorts, err := b.ExtractExposedPorts(desiredApp)
	if err != nil {
		return nil, err
	}

	switch desiredApp.HealthCheckType {
	case cc_messages.PortHealthCheckType, cc_messages.UnspecifiedHealthCheckType:
		monitor = models.Timeout(getParallelAction(desiredAppPorts, "vcap"), 30*time.Second)
	}

	downloadAction := &models.DownloadAction{
		From:     desiredApp.DropletUri,
		To:       ".",
		CacheKey: fmt.Sprintf("droplets-%s", lrpGuid),
		User:     "vcap",
	}

	if desiredApp.DropletHash != "" {
		downloadAction.ChecksumAlgorithm = "sha1"
		downloadAction.ChecksumValue = desiredApp.DropletHash
	}

	setup = append(setup, downloadAction)
	actions = append(actions, &models.RunAction{
		User: "vcap",
		Path: "/tmp/lifecycle/launcher",
		Args: append(
			[]string{"app"},
			desiredApp.StartCommand,
			desiredApp.ExecutionMetadata,
		),
		Env:       createLrpEnv(desiredApp.Environment, desiredAppPorts),
		LogSource: getAppLogSource(desiredApp.LogSource),
		ResourceLimits: &models.ResourceLimits{
			Nofile: &numFiles,
		},
	})

	desiredAppRoutingInfo, err := helpers.CCRouteInfoToRoutes(desiredApp.RoutingInfo, desiredAppPorts)
	if err != nil {
		buildLogger.Error("marshaling-cc-route-info-failed", err)
		return nil, err
	}

	if desiredApp.AllowSSH {
		hostKeyPair, err := b.config.KeyFactory.NewKeyPair(1024)
		if err != nil {
			buildLogger.Error("new-host-key-pair-failed", err)
			return nil, err
		}

		userKeyPair, err := b.config.KeyFactory.NewKeyPair(1024)
		if err != nil {
			buildLogger.Error("new-user-key-pair-failed", err)
			return nil, err
		}

		actions = append(actions, &models.RunAction{
			User: "vcap",
			Path: "/tmp/lifecycle/diego-sshd",
			Args: []string{
				"-address=" + fmt.Sprintf("0.0.0.0:%d", DefaultSSHPort),
				"-hostKey=" + hostKeyPair.PEMEncodedPrivateKey(),
				"-authorizedKey=" + userKeyPair.AuthorizedKey(),
				"-inheritDaemonEnv",
				"-logLevel=fatal",
			},
			Env: createLrpEnv(desiredApp.Environment, desiredAppPorts),
			ResourceLimits: &models.ResourceLimits{
				Nofile: &numFiles,
			},
		})

		sshRoutePayload, err := json.Marshal(ssh_routes.SSHRoute{
			ContainerPort:   2222,
			PrivateKey:      userKeyPair.PEMEncodedPrivateKey(),
			HostFingerprint: hostKeyPair.Fingerprint(),
		})

		if err != nil {
			buildLogger.Error("marshaling-ssh-route-failed", err)
			return nil, err
		}

		sshRouteMessage := json.RawMessage(sshRoutePayload)
		desiredAppRoutingInfo[ssh_routes.DIEGO_SSH] = &sshRouteMessage
		desiredAppPorts = append(desiredAppPorts, DefaultSSHPort)
	}

	setupAction := models.Serial(setup...)
	actionAction := models.Codependent(actions...)

	return &models.DesiredLRP{
		Privileged: b.config.PrivilegedContainers,

		Domain: cc_messages.AppLRPDomain,

		ProcessGuid: lrpGuid,
		Instances:   int32(desiredApp.NumInstances),
		Routes:      &desiredAppRoutingInfo,
		Annotation:  desiredApp.ETag,

		CpuWeight: cpuWeight(desiredApp.MemoryMB),

		MemoryMb: int32(desiredApp.MemoryMB),
		DiskMb:   int32(desiredApp.DiskMB),

		Ports: desiredAppPorts,

		RootFs: rootFSPath,

		LogGuid:   desiredApp.LogGuid,
		LogSource: LRPLogSource,

		MetricsGuid: desiredApp.LogGuid,

		EnvironmentVariables: containerEnvVars,
		CachedDependencies:   cachedDependencies,
		Setup:                models.WrapAction(setupAction),
		Action:               models.WrapAction(actionAction),
		Monitor:              models.WrapAction(monitor),

		//StartTimeout: uint32(desiredApp.HealthCheckTimeoutInSeconds),

		EgressRules:        desiredApp.EgressRules,
		Network:            desiredApp.Network,
		LegacyDownloadUser: "vcap",

		TrustedSystemCertificatesPath: TrustedSystemCertificatesPath,
		VolumeMounts:                  desiredApp.VolumeMounts,
	}, nil
}

func (b BuildpackRecipeBuilder) ExtractExposedPorts(desiredApp *cc_messages.DesireAppRequestFromCC) ([]uint32, error) {
	return getDesiredAppPorts(desiredApp.Ports), nil
}

func GetSpaceGuid(desireAppMessage cc_messages.DesireAppRequestFromCC) (string, error) {
	var vcapApplications struct {
		SpaceGuid string `json:"space_id"`
	}

	for _, env := range desireAppMessage.Environment {
		if env.Name == "VCAP_APPLICATION" {
			err := json.Unmarshal([]byte(env.Value), &vcapApplications)
			return vcapApplications.SpaceGuid, err
		}
	}

	return "", errors.New("Missing space guid")
}
