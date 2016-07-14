package transformer

import (
	"fmt"
	"strings"

	"github.com/cloudfoundry-incubator/runtime-schema/cc_messages"
	"github.com/pivotal-golang/lager"
	"k8s.io/kubernetes/pkg/api"
)

// transform from desired app to ReplicationController format
func DesiredAppToRC(logger lager.Logger,
	desiredApp cc_messages.DesireAppRequestFromCC) (*api.ReplicationController, error) {
	if desiredApp.DockerImageUrl != "" {
		// push docker image url to kube image
		imageUrl, err := PushImageToKubeRegistry(desiredApp.DockerImageUrl)
		if err != nil {
			logger.Fatal("failed-to-push-image", err)
		}

		// transform to RC
		return DesiredAppImageToRC(logger, desiredApp, imageUrl)
	} else {
		return DesiredAppDropletToRC(logger, desiredApp)
	}
}

func PushImageToKubeRegistry(imageUrl string) (string, error) {
	// TODO: if public dockerhub image is used, we can return as it is
	// TODO: if it is a private registry, we would need to push that image to kube registry
	return imageUrl, nil
}

func DesiredAppDropletToRC(logger lager.Logger, desiredApp cc_messages.DesireAppRequestFromCC) (*api.ReplicationController, error) {
	var env string
	var rcGUID string

	// TODO: remove this hack...
	dropletURI := strings.Replace(desiredApp.DropletUri, "cloud-controller-ng.service.cf.internal", "10.244.0.138", 1)
	for _, envVar := range desiredApp.Environment {
		if envVar.Name == "VCAP_APPLICATION" {
			// ignore for now
		} else if envVar.Name == "VCAP_SERVICES" || envVar.Name == "MEMORY_LIMIT" {
			// ignore it for now
		} else {
			env = strings.TrimPrefix(fmt.Sprintf("%s,%s=%s", env, envVar.Name, envVar.Value), ",")
		}
	}

	// kube requires replication controller name < 63
	if len(desiredApp.ProcessGuid) >= 63 {
		rcGUID = desiredApp.ProcessGuid[:60]
	} else {
		rcGUID = desiredApp.ProcessGuid
	}

	rc := &api.ReplicationController{
		ObjectMeta: api.ObjectMeta{
			Name: rcGUID,
		},
		Spec: api.ReplicationControllerSpec{
			Replicas: int32(desiredApp.NumInstances),
			Selector: map[string]string{"name": rcGUID},
			Template: &api.PodTemplateSpec{
				ObjectMeta: api.ObjectMeta{
					Name:   rcGUID,
					Labels: map[string]string{"name": rcGUID},
				},
				Spec: api.PodSpec{
					Containers: []api.Container{{
						Name:  fmt.Sprintf("%s-r", rcGUID),
						Image: "linsun/k8s-runner:latest",
						Env: []api.EnvVar{
							{Name: "STARTCMD", Value: desiredApp.StartCommand},
							{Name: "ENVVARS", Value: env},
							{Name: "PORT", Value: "8080"},
							{Name: "DROPLETURI", Value: dropletURI},
						},
						VolumeMounts: []api.VolumeMount{{
							Name:      "etc-hosts-volume",
							MountPath: "/etc/hosts",
						}},
					}},
					Volumes: []api.Volume{{
						Name: "etc-hosts-volume",
						VolumeSource: api.VolumeSource{
							HostPath: &api.HostPathVolumeSource{Path: "/tmp/vcap/etc/hosts"},
						},
					}},
				},
			},
		},
	}

	return rc, nil
}

func DesiredAppImageToRC(logger lager.Logger, desiredApp cc_messages.DesireAppRequestFromCC, imageUrl string) (*api.ReplicationController, error) {
	var env string
	var rcGUID string

	for _, envVar := range desiredApp.Environment {
		if envVar.Name == "VCAP_APPLICATION" {
			// ignore
		} else if envVar.Name == "VCAP_SERVICES" || envVar.Name == "MEMORY_LIMIT" {
			// ignore it for now
		} else {
			env = strings.TrimPrefix(fmt.Sprintf("%s,%s=%s", env, envVar.Name, envVar.Value), ",")
		}
	}

	// kube requires replication controller name < 63
	if len(desiredApp.ProcessGuid) >= 60 {
		rcGUID = desiredApp.ProcessGuid[:60]
	} else {
		rcGUID = desiredApp.ProcessGuid
	}

	rc := &api.ReplicationController{
		ObjectMeta: api.ObjectMeta{
			Name: rcGUID,
		},
		Spec: api.ReplicationControllerSpec{
			Replicas: int32(desiredApp.NumInstances),
			Selector: map[string]string{"name": rcGUID},
			Template: &api.PodTemplateSpec{
				ObjectMeta: api.ObjectMeta{
					Name:   rcGUID,
					Labels: map[string]string{"name": rcGUID},
				},
				Spec: api.PodSpec{
					Containers: []api.Container{{
						Name:  rcGUID,
						Image: imageUrl,
						Env: []api.EnvVar{
							{Name: "STARTCMD", Value: desiredApp.StartCommand},
							{Name: "ENVVARS", Value: env},
							{Name: "PORT", Value: "8080"},
						},
					}},
				},
			},
		},
	}

	return rc, nil
}
