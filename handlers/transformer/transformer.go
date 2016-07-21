package transformer

import (
	"fmt"
	"strings"

	"github.com/cloudfoundry-incubator/nsync/helpers"
	"github.com/cloudfoundry-incubator/runtime-schema/cc_messages"
	"github.com/pivotal-golang/lager"
	"k8s.io/kubernetes/pkg/api/v1"
)

// transform from desired app to ReplicationController format
func DesiredAppToRC(logger lager.Logger, processGuid helpers.ProcessGuid, desiredApp cc_messages.DesireAppRequestFromCC) (*v1.ReplicationController, error) {
	if desiredApp.DockerImageUrl != "" {
		return DesiredAppImageToRC(logger, processGuid, desiredApp)
	}
	return DesiredAppDropletToRC(logger, processGuid, desiredApp)
}

func DesiredAppDropletToRC(logger lager.Logger, processGuid helpers.ProcessGuid, desiredApp cc_messages.DesireAppRequestFromCC) (*v1.ReplicationController, error) {
	shortenedProcessGuid := processGuid.ShortenedGuid()
	dnsHack := true

	var env string
	for _, envVar := range desiredApp.Environment {
		if envVar.Name == "VCAP_APPLICATION" {
			// ignore for now
		} else if envVar.Name == "VCAP_SERVICES" || envVar.Name == "MEMORY_LIMIT" {
			// ignore it for now
		} else {
			env = strings.TrimPrefix(fmt.Sprintf("%s,%s=%s", env, envVar.Name, envVar.Value), ",")
		}
	}

	env = strings.TrimPrefix(env+",PORT=8080", ",")

	if dnsHack == true {
		dropletURI := strings.Replace(desiredApp.DropletUri, "cloud-controller-ng.service.cf.internal", "10.244.0.138", 1)
		rc := &v1.ReplicationController{
			ObjectMeta: v1.ObjectMeta{Name: shortenedProcessGuid},
			Spec: v1.ReplicationControllerSpec{
				Replicas: helpers.Int32Ptr(desiredApp.NumInstances),
				Selector: map[string]string{"name": shortenedProcessGuid},
				Template: &v1.PodTemplateSpec{
					ObjectMeta: v1.ObjectMeta{
						Name: shortenedProcessGuid,
						Labels: map[string]string{
							"name": shortenedProcessGuid,
						},
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{{
							Name:  fmt.Sprintf("%s-r", shortenedProcessGuid),
							Image: "linsun/k8s-runner:latest",
							Env: []v1.EnvVar{
								{Name: "STARTCMD", Value: desiredApp.StartCommand},
								{Name: "ENVVARS", Value: env},
								{Name: "PORT", Value: "8080"},
								{Name: "DROPLETURI", Value: dropletURI},
							},
							VolumeMounts: []v1.VolumeMount{{
								Name:      "etc-hosts-volume",
								MountPath: "/etc/hosts",
							}},
						}},
						Volumes: []v1.Volume{{
							Name: "etc-hosts-volume",
							VolumeSource: v1.VolumeSource{
								HostPath: &v1.HostPathVolumeSource{Path: "/tmp/etc/hosts"},
							},
						}},
					},
				},
			},
		}
		return rc, nil
	} else {
		rc := &v1.ReplicationController{
			ObjectMeta: v1.ObjectMeta{Name: shortenedProcessGuid},
			Spec: v1.ReplicationControllerSpec{
				Replicas: helpers.Int32Ptr(desiredApp.NumInstances),
				Selector: map[string]string{"name": shortenedProcessGuid},
				Template: &v1.PodTemplateSpec{
					ObjectMeta: v1.ObjectMeta{
						Name: shortenedProcessGuid,
						Labels: map[string]string{
							"name": shortenedProcessGuid,
						},
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{{
							Name:  fmt.Sprintf("%s-r", shortenedProcessGuid),
							Image: "linsun/k8s-runner:latest",
							Env: []v1.EnvVar{
								{Name: "STARTCMD", Value: desiredApp.StartCommand},
								{Name: "ENVVARS", Value: env},
								{Name: "PORT", Value: "8080"},
								{Name: "DROPLETURI", Value: desiredApp.DropletUri},
							},
						}},
					},
				},
			},
		}
		return rc, nil
	}

}

func DesiredAppImageToRC(logger lager.Logger, processGuid helpers.ProcessGuid, desiredApp cc_messages.DesireAppRequestFromCC) (*v1.ReplicationController, error) {
	shortenedProcessGuid := processGuid.ShortenedGuid()

	var env string
	for _, envVar := range desiredApp.Environment {
		if envVar.Name == "VCAP_APPLICATION" {
			// ignore
		} else if envVar.Name == "VCAP_SERVICES" || envVar.Name == "MEMORY_LIMIT" {
			// ignore it for now
		} else {
			env = strings.TrimPrefix(fmt.Sprintf("%s,%s=%s", env, envVar.Name, envVar.Value), ",")
		}
	}

	rc := &v1.ReplicationController{
		ObjectMeta: v1.ObjectMeta{
			Name: shortenedProcessGuid,
		},
		Spec: v1.ReplicationControllerSpec{
			Replicas: helpers.Int32Ptr(desiredApp.NumInstances),
			Selector: map[string]string{"name": shortenedProcessGuid},
			Template: &v1.PodTemplateSpec{
				ObjectMeta: v1.ObjectMeta{
					Name:   shortenedProcessGuid,
					Labels: map[string]string{"name": shortenedProcessGuid},
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{{
						Name:  shortenedProcessGuid,
						Image: desiredApp.DockerImageUrl,
						Env: []v1.EnvVar{
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
