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
		routingInfo, err := cc_messages.CCHTTPRoutes{
			{Hostname: "route1", Port: 8080},
			{Hostname: "route2"},
		}.CCRouteInfo()

		Expect(err).NotTo(HaveOccurred())

		desiredApp = cc_messages.DesireAppRequestFromCC{
			ProcessGuid:  "e9640a75-9ddf-4351-bccd-21264640c156-c542db92-6d3a-43c6-b975-f8a7501ac651",
			DropletUri:   "source-url-1",
			Stack:        "stack-1",
			StartCommand: "start-command-1",
			Environment: []*models.EnvironmentVariable{
				{Name: "env-key-1", Value: "env-value-1"},
				{Name: "env-key-2", Value: "env-value-2"},
				{Name: "VCAP_APPLICATION", Value: "{\"application_name\":\"dora\",\"application_uris\":[\"dora.bosh-lite.com\"],\"name\":\"dora\",\"space_name\":\"diego\",\"space_id\":\"c99b5d70-3b63-4cda-b15c-fd9dc147967b\",\"uris\":[\"dora.bosh-lite.com\"],\"application_id\":\"e9640a75-9ddf-4351-bccd-21264640c156\",\"version\":\"c542db92-6d3a-43c6-b975-f8a7501ac651\",\"application_version\":\"c542db92-6d3a-43c6-b975-f8a7501ac651\"}"},
				{Name: "VCAP_SERVICES", Value: "{}"},
			},
			MemoryMB:        256,
			DiskMB:          1024,
			FileDescriptors: 16,
			NumInstances:    2,
			LogGuid:         "log-guid-1",
			ETag:            "1234567.1890",
			Ports:           []uint32{8080},
			RoutingInfo:     routingInfo,
		}

		desiredApp2 = cc_messages.DesireAppRequestFromCC{
			ProcessGuid:    "e9640a75-9ddf-4351-bccd-21264640c156-c542db92-6d3a-43c6-b975-f8a7501ac651",
			DockerImageUrl: "test/ubuntu:latest",
			StartCommand:   "start-command-1",
			Environment: []*models.EnvironmentVariable{
				{Name: "env-key-1", Value: "env-value-1"},
				{Name: "env-key-2", Value: "env-value-2"},
				{Name: "VCAP_APPLICATION", Value: "{\"application_name\":\"dora\",\"application_uris\":[\"dora.bosh-lite.com\"],\"name\":\"dora\",\"space_name\":\"diego\",\"space_id\":\"c99b5d70-3b63-4cda-b15c-fd9dc147967b\",\"uris\":[\"dora.bosh-lite.com\"],\"application_id\":\"e9640a75-9ddf-4351-bccd-21264640c156\",\"version\":\"c542db92-6d3a-43c6-b975-f8a7501ac651\",\"application_version\":\"c542db92-6d3a-43c6-b975-f8a7501ac651\"}"},
				{Name: "VCAP_SERVICES", Value: "{}"},
			},
			MemoryMB:        256,
			DiskMB:          1024,
			FileDescriptors: 16,
			NumInstances:    2,
			LogGuid:         "log-guid-1",
			ETag:            "1234567.1890",
			Ports:           []uint32{8080},
		}

		rcGUID := "e9640a75-9ddf-4351-bccd-21264640c156-c542db92-6d3a-43c6-b975-f8a7501ac651"[:60]
		expectedRC = &api.ReplicationController{
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
							Name:  rcGUID + "-r",
							Image: "linsun/k8s-runner:latest",
							Env: []api.EnvVar{
								{Name: "STARTCMD", Value: "start-command-1"},
								{Name: "ENVVARS", Value: "env-key-1=env-value-1,env-key-2=env-value-2,PORT=8080"},
								{Name: "PORT", Value: "8080"},
								{Name: "DROPLETURI", Value: "source-url-1"},
							},
						}},
					}},
			},
		}

		expectedRC2 = &api.ReplicationController{
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
