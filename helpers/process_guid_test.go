package helpers_test

import (
	"fmt"

	"k8s.io/kubernetes/pkg/util/validation"

	"github.com/cloudfoundry-incubator/nsync/helpers"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("ProcessGuid", func() {
	var processGuid helpers.ProcessGuid
	var appGuid, appVersion string
	var shortenedProcessGuid string

	BeforeEach(func() {
		appGuid = "8d58c09b-b305-4f16-bcfe-b78edcb77100"
		appVersion = "3f258eb0-9dac-460c-a424-b43fe92bee27"
		shortenedProcessGuid = "rvmmbg5tavhrnph6w6hnzn3raa-h4sy5me5vrdazjbewq76sk7oe4"
	})

	Describe("NewProcessGuid", func() {
		var err error

		JustBeforeEach(func() {
			processGuid, err = helpers.NewProcessGuid(fmt.Sprintf("%s-%s", appGuid, appVersion))
		})

		It("breaks the guid apart", func() {
			Expect(err).NotTo(HaveOccurred())
			Expect(processGuid.AppGuid.String()).To(Equal(appGuid))
			Expect(processGuid.AppVersion.String()).To(Equal(appVersion))
		})

		Context("when the source guid is too short", func() {
			BeforeEach(func() {
				appGuid = "guid"
				appVersion = "version"
			})

			It("returns an error", func() {
				Expect(err).To(MatchError("invalid process guid"))
			})
		})

		Context("when the source app guid is invalid", func() {
			BeforeEach(func() {
				appGuid = "this-is-an-invalid-app-guid-with-len"
			})

			It("returns an error", func() {
				Expect(err).To(MatchError("Invalid UUID string"))
			})
		})

		Context("when the source app guid is invalid", func() {
			BeforeEach(func() {
				appVersion = "this-is-an-invalid-app-version-with-"
			})

			It("returns an error", func() {
				Expect(err).To(MatchError("Invalid UUID string"))
			})
		})
	})

	Describe("ShortenedGuid", func() {
		BeforeEach(func() {
			pg, err := helpers.NewProcessGuid(fmt.Sprintf("%s-%s", appGuid, appVersion))
			Expect(err).NotTo(HaveOccurred())
			processGuid = pg
		})

		It("returns an encoded version of the process guid", func() {
			Expect(processGuid.ShortenedGuid()).To(Equal(shortenedProcessGuid))
		})

		It("is a valid kubernetes dns label", func() {
			Expect(validation.IsDNS1123Label(processGuid.ShortenedGuid())).To(BeEmpty())
		})
	})

	Describe("String", func() {
		BeforeEach(func() {
			pg, err := helpers.NewProcessGuid(fmt.Sprintf("%s-%s", appGuid, appVersion))
			Expect(err).NotTo(HaveOccurred())
			processGuid = pg
		})

		It("returns the normal representation of a process guid", func() {
			Expect(processGuid.String()).To(Equal(appGuid + "-" + appVersion))
		})
	})

	Describe("decode a shortened process guid to the original guid string", func() {
		BeforeEach(func() {
			pg, err := helpers.NewProcessGuid(fmt.Sprintf("%s-%s", appGuid, appVersion))
			Expect(err).NotTo(HaveOccurred())
			processGuid = pg
		})

		It("returns the correct process guid", func() {
			decodedProcessGuid, err := helpers.DecodeProcessGuid(shortenedProcessGuid)
			Expect(err).NotTo(HaveOccurred())
			Expect(decodedProcessGuid.String()).To(Equal(processGuid.String()))
		})
	})

	Describe("errors when decode a shortened process guid to the original guid string", func() {
		BeforeEach(func() {
			shortenedProcessGuid = "this-is-an-invalid-shortened-guid-with-len"
		})

		It("returns an error", func() {
			_, err := helpers.DecodeProcessGuid(shortenedProcessGuid)
			Expect(err).To(MatchError("invalid shortened process guid"))
		})
	})
})
