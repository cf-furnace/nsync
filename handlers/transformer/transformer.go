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
		return DesiredAppImageToRC(desiredApp, imageUrl)
	} else {
		return DesiredAppDropletToRC(desiredApp)
	}
}

func PushImageToKubeRegistry(imageUrl string) (string, error) {
	// TODO: if public dockerhub image is used, we can return as it is
	// TODO: if it is a private registry, we would need to push that image to kube registry
	return imageUrl, nil
}

func DesiredAppDropletToRC(desiredApp cc_messages.DesireAppRequestFromCC) (*api.ReplicationController, error) {
	var env string
	for _, envVar := range desiredApp.Environment {
		env = strings.TrimPrefix(fmt.Sprintf("%s,%s=%s", env, envVar.Name, envVar.Value), ",")
	}

	rc := &api.ReplicationController{
		ObjectMeta: api.ObjectMeta{
			Name: desiredApp.ProcessGuid,
		},
		Spec: api.ReplicationControllerSpec{
			Replicas: int32(desiredApp.NumInstances),
			Selector: map[string]string{"name": desiredApp.ProcessGuid},
			Template: &api.PodTemplateSpec{
				ObjectMeta: api.ObjectMeta{
					Name:   desiredApp.ProcessGuid,
					Labels: map[string]string{"name": desiredApp.ProcessGuid},
				},
				Spec: api.PodSpec{
					Containers: []api.Container{{
						Name:  fmt.Sprintf("%s-data", desiredApp.ProcessGuid),
						Image: fmt.Sprintf("localhost:5000/linsun/%s-data:latest", desiredApp.ProcessGuid),
						Lifecycle: &api.Lifecycle{
							PostStart: &api.Handler{
								Exec: &api.ExecAction{
									Command: []string{"cp", "/droplet.tgz", "/app"},
								},
							},
						},
						VolumeMounts: []api.VolumeMount{{
							Name:      "app-volume",
							MountPath: "/app",
						}},
					}, {
						Name:  fmt.Sprintf("%s-runner", desiredApp.ProcessGuid),
						Image: "localhost:5000/default/k8s-runner:latest",
						Env: []api.EnvVar{
							{Name: "STARTCMD", Value: desiredApp.StartCommand},
							{Name: "ENVVARS", Value: env},
							{Name: "PORT", Value: "8080"},
						},
						VolumeMounts: []api.VolumeMount{{
							Name:      "app-volume",
							MountPath: "/app/droplet",
						}},
					}},
					Volumes: []api.Volume{{
						Name: "app-volume",
						VolumeSource: api.VolumeSource{
							EmptyDir: &api.EmptyDirVolumeSource{},
						},
					}},
				},
			},
		},
	}

	return rc, nil
}

func DesiredAppImageToRC(desiredApp cc_messages.DesireAppRequestFromCC, imageUrl string) (*api.ReplicationController, error) {
	var env string
	for _, envVar := range desiredApp.Environment {
		env = strings.TrimPrefix(fmt.Sprintf("%s,%s=%s", env, envVar.Name, envVar.Value), ",")
	}

	rc := &api.ReplicationController{
		ObjectMeta: api.ObjectMeta{
			Name: desiredApp.ProcessGuid,
		},
		Spec: api.ReplicationControllerSpec{
			Replicas: int32(desiredApp.NumInstances),
			Selector: map[string]string{"name": desiredApp.ProcessGuid},
			Template: &api.PodTemplateSpec{
				ObjectMeta: api.ObjectMeta{
					Name:   desiredApp.ProcessGuid,
					Labels: map[string]string{"name": desiredApp.ProcessGuid},
				},
				Spec: api.PodSpec{
					Containers: []api.Container{{
						Name:  desiredApp.ProcessGuid,
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
