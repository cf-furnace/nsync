package transformer_test

import (
	"os"

	"github.com/cloudfoundry-incubator/bbs/models"
	"github.com/cloudfoundry-incubator/nsync/handlers/transformer"
	"github.com/cloudfoundry-incubator/runtime-schema/cc_messages"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/pivotal-golang/lager"
	"k8s.io/kubernetes/pkg/api"
)

var _ = Describe("Transformer", func() {
	var desiredApp, desiredApp2 cc_messages.DesireAppRequestFromCC
	//var expectedPod *api.Pod
	var expectedRC, expectedRC2 *api.ReplicationController
	logger := lager.NewLogger("transformer_test")
	logger.RegisterSink(lager.NewWriterSink(os.Stderr, lager.DEBUG))

	BeforeEach(func() {
		desiredApp = cc_messages.DesireAppRequestFromCC{
			ProcessGuid:  "process-guid-1",
			DropletUri:   "source-url-1",
			Stack:        "stack-1",
			StartCommand: "start-command-1",
			Environment: []*models.EnvironmentVariable{
				{Name: "env-key-1", Value: "env-value-1"},
				{Name: "env-key-2", Value: "env-value-2"},
			},
			MemoryMB:        256,
			DiskMB:          1024,
			FileDescriptors: 16,
			NumInstances:    2,
			LogGuid:         "log-guid-1",
			ETag:            "1234567.1890",
			Ports:           []uint32{8080},
		}

		desiredApp2 = cc_messages.DesireAppRequestFromCC{
			ProcessGuid:    "process-guid-1",
			DockerImageUrl: "test/ubuntu:latest",
			StartCommand:   "start-command-1",
			Environment: []*models.EnvironmentVariable{
				{Name: "env-key-1", Value: "env-value-1"},
				{Name: "env-key-2", Value: "env-value-2"},
			},
			MemoryMB:        256,
			DiskMB:          1024,
			FileDescriptors: 16,
			NumInstances:    2,
			LogGuid:         "log-guid-1",
			ETag:            "1234567.1890",
			Ports:           []uint32{8080},
		}

		expectedRC = &api.ReplicationController{
			ObjectMeta: api.ObjectMeta{
				Name: "process-guid-1",
			},
			Spec: api.ReplicationControllerSpec{
				Replicas: int32(desiredApp.NumInstances),
				Selector: map[string]string{"name": "process-guid-1"},
				Template: &api.PodTemplateSpec{
					ObjectMeta: api.ObjectMeta{
						Name:   "process-guid-1",
						Labels: map[string]string{"name": "process-guid-1"},
					},
					Spec: api.PodSpec{
						Containers: []api.Container{{
							Name:  "process-guid-1-data",
							Image: "localhost:5000/linsun/process-guid-1-data:latest",
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
							Name:  "process-guid-1-runner",
							Image: "localhost:5000/default/k8s-runner:latest",
							Env: []api.EnvVar{
								{Name: "STARTCMD", Value: "start-command-1"},
								{Name: "ENVVARS", Value: "env-key-1=env-value-1,env-key-2=env-value-2"},
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
					}},
			},
		}

		expectedRC2 = &api.ReplicationController{
			ObjectMeta: api.ObjectMeta{
				Name: "process-guid-1",
			},
			Spec: api.ReplicationControllerSpec{
				Replicas: int32(desiredApp.NumInstances),
				Selector: map[string]string{"name": "process-guid-1"},
				Template: &api.PodTemplateSpec{
					ObjectMeta: api.ObjectMeta{
						Name:   "process-guid-1",
						Labels: map[string]string{"name": "process-guid-1"},
					},
					Spec: api.PodSpec{
						Containers: []api.Container{{
							Name:  "process-guid-1",
							Image: "test/ubuntu:latest",
							Env: []api.EnvVar{
								{Name: "STARTCMD", Value: "start-command-1"},
								{Name: "ENVVARS", Value: "env-key-1=env-value-1,env-key-2=env-value-2"},
								{Name: "PORT", Value: "8080"},
							},
						}},
					}},
			},
		}
	})

	It("generates the expected kubernetes pod struct", func() {
		rc, err := transformer.DesiredAppToRC(logger, desiredApp)
		Expect(err).NotTo(HaveOccurred())
		Expect(rc).To(Equal(expectedRC))
	})

	It("generates the expected kubernetes pod struct", func() {
		rc, err := transformer.DesiredAppToRC(logger, desiredApp2)
		Expect(err).NotTo(HaveOccurred())
		Expect(rc).To(Equal(expectedRC2))
	})
})
