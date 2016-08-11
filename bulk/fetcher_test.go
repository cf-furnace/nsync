package bulk_test

import (
	"net/http"
	"net/url"
	"time"

	"github.com/cloudfoundry-incubator/bbs/models"
	"github.com/cloudfoundry-incubator/nsync/bulk"
	"github.com/cloudfoundry-incubator/nsync/helpers"
	"github.com/cloudfoundry-incubator/runtime-schema/cc_messages"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/ghttp"
	"github.com/pivotal-golang/lager/lagertest"
)

var _ = Describe("Fetcher", func() {
	var (
		fetcher    bulk.Fetcher
		fakeCC     *ghttp.Server
		logger     *lagertest.TestLogger
		httpClient *http.Client

		cancel       chan struct{}
		processGuid1 helpers.ProcessGuid
		processGuid2 helpers.ProcessGuid
		processGuid3 helpers.ProcessGuid
		err          error
	)

	BeforeEach(func() {
		fakeCC = ghttp.NewServer()
		logger = lagertest.NewTestLogger("test")

		cancel = make(chan struct{})
		httpClient = &http.Client{Timeout: time.Second}

		fetcher = &bulk.CCFetcher{
			BaseURI:   fakeCC.URL(),
			BatchSize: 2,
			Username:  "the-username",
			Password:  "the-password",
		}

		processGuid1, err = helpers.NewProcessGuid(generateProcessGuid())
		Expect(err).NotTo(HaveOccurred())

		processGuid2, err = helpers.NewProcessGuid(generateProcessGuid())
		Expect(err).NotTo(HaveOccurred())

		processGuid3, err = helpers.NewProcessGuid(generateProcessGuid())
		Expect(err).NotTo(HaveOccurred())
	})

	Describe("Fetching Desired App Fingerprints", func() {
		var resultsChan <-chan []cc_messages.CCDesiredAppFingerprint
		var errorsChan <-chan error

		JustBeforeEach(func() {
			resultsChan, errorsChan = fetcher.FetchFingerprints(logger, cancel, httpClient)
		})

		AfterEach(func() {
			Eventually(resultsChan).Should(BeClosed())
			Eventually(errorsChan).Should(BeClosed())
		})

		Context("when retrieving fingerprints", func() {
			BeforeEach(func() {
				fakeCC.AppendHandlers(
					ghttp.CombineHandlers(
						ghttp.VerifyRequest("GET", "/internal/bulk/apps", "batch_size=2&format=fingerprint&token={}"),
						ghttp.VerifyBasicAuth("the-username", "the-password"),
						ghttp.RespondWith(200, `{
						"token": {"id":"the-token-id"},
						"fingerprints": [
							{
							  "process_guid": "`+processGuid1.String()+`",
								"etag": "1234567.890"
							},
							{
							  "process_guid": "`+processGuid2.String()+`",
								"etag": "2345678.901"
							}
						]
					}`),
					),
					ghttp.CombineHandlers(
						ghttp.VerifyRequest("GET", "/internal/bulk/apps", `batch_size=2&format=fingerprint&token={"id":"the-token-id"}`),
						ghttp.VerifyBasicAuth("the-username", "the-password"),
						ghttp.RespondWith(200, `{
							"token": {"id":"another-token-id"},
							"fingerprints": [
								{
									"process_guid": "`+processGuid3.String()+`",
									"etag": "3456789.012"
								}
							]
						}`),
					),
				)
			})

			It("retrieves fingerprints of all apps desired by CC", func() {
				Eventually(resultsChan).Should(Receive(ConsistOf(
					cc_messages.CCDesiredAppFingerprint{
						ProcessGuid: processGuid1.String(),
						ETag:        "1234567.890",
					},
					cc_messages.CCDesiredAppFingerprint{
						ProcessGuid: processGuid2.String(),
						ETag:        "2345678.901",
					},
				)))

				Eventually(resultsChan).Should(Receive(ConsistOf(
					cc_messages.CCDesiredAppFingerprint{
						ProcessGuid: processGuid3.String(),
						ETag:        "3456789.012",
					})))

				Eventually(resultsChan).Should(BeClosed())
				Expect(fakeCC.ReceivedRequests()).To(HaveLen(2))
			})
		})

		Context("when the response is missing a bulk token", func() {
			BeforeEach(func() {
				fakeCC.AppendHandlers(
					ghttp.CombineHandlers(
						ghttp.VerifyRequest("GET", "/internal/bulk/apps", "batch_size=2&format=fingerprint&token={}"),
						ghttp.VerifyBasicAuth("the-username", "the-password"),
						ghttp.RespondWith(200, `{
							"fingerprints": [
								{
									"process_guid": "`+processGuid1.String()+`",
									"etag": "1234567.890"
								},
								{
									"process_guid": "`+processGuid2.String()+`",
									"etag": "2345678.901"
								}
							]
						}`),
					),
				)
			})

			It("sends an error on the error channel", func() {
				Eventually(resultsChan).Should(Receive())
				Eventually(errorsChan).Should(Receive(MatchError("token not included in response")))
			})

			It("rerturns the fingerprints that were retrieved", func() {
				Eventually(resultsChan).Should(Receive(ConsistOf(
					cc_messages.CCDesiredAppFingerprint{
						ProcessGuid: processGuid1.String(),
						ETag:        "1234567.890",
					},
					cc_messages.CCDesiredAppFingerprint{
						ProcessGuid: processGuid2.String(),
						ETag:        "2345678.901",
					},
				)))
			})
		})

		Context("when the API times out", func() {
			ccResponseTime := 100 * time.Millisecond

			BeforeEach(func() {
				fakeCC.AppendHandlers(func(w http.ResponseWriter, req *http.Request) {
					time.Sleep(ccResponseTime)

					w.Write([]byte(`{
						"token": {"id":"another-token-id"},
						"fingerprints": [
							{
								"process_guid": ` + processGuid1.String() + `,
								"etag": "1234567.890"
							},
							{
								"process_guid": ` + processGuid2.String() + `,
								"etag": "2345678.901"
							}
						]
					}`))
				})

				httpClient = &http.Client{Timeout: ccResponseTime / 2}
			})

			It("sends an error on the error channel", func() {
				Eventually(errorsChan).Should(Receive(BeAssignableToTypeOf(&url.Error{})))
			})
		})

		Context("when the API returns an error response", func() {
			BeforeEach(func() {
				fakeCC.AppendHandlers(ghttp.RespondWith(403, ""))
			})

			It("sends an error on the error channel", func() {
				Eventually(errorsChan).Should(Receive(HaveOccurred()))
			})
		})

		Context("when the server responds with invalid JSON", func() {
			BeforeEach(func() {
				fakeCC.AppendHandlers(ghttp.RespondWith(200, "{"))
			})

			It("sends an error on the error channel", func() {
				Eventually(errorsChan).Should(Receive(HaveOccurred()))
			})
		})

		Describe("cancelling", func() {
			BeforeEach(func() {
				fakeCC.AppendHandlers(
					ghttp.CombineHandlers(
						ghttp.VerifyRequest("GET", "/internal/bulk/apps", "batch_size=2&format=fingerprint&token={}"),
						ghttp.RespondWith(200, `{
							"token": {"id":"another-token-id"},
							"fingerprints": [
								{
									"process_guid": `+processGuid3.String()+`,
									"etag": "3456789.012"
								}
							]
						}`),
					),
				)
			})

			Context("when waiting to send fingerprints", func() {
				It("exits when cancelled", func() {
					close(cancel)
					Eventually(resultsChan).Should(BeClosed())
					Eventually(errorsChan).Should(BeClosed())
				})
			})
		})
	})

	Describe("Fetching Desired App Request Messages from CC", func() {
		var (
			cancel           chan struct{}
			fingerprintsChan chan []cc_messages.CCDesiredAppFingerprint

			resultsChan <-chan []cc_messages.DesireAppRequestFromCC
			errorsChan  <-chan error
		)

		BeforeEach(func() {
			cancel = make(chan struct{})
			fingerprintsChan = make(chan []cc_messages.CCDesiredAppFingerprint, 1)
		})

		JustBeforeEach(func() {
			resultsChan, errorsChan = fetcher.FetchDesiredApps(logger, cancel, httpClient, fingerprintsChan)
		})

		Context("when retrieving desired app messages", func() {
			var desireRequests []cc_messages.DesireAppRequestFromCC

			BeforeEach(func() {
				routeInfo1, err := cc_messages.CCHTTPRoutes{
					{Hostname: "route-1"},
					{Hostname: "route-2"},
				}.CCRouteInfo()
				Expect(err).NotTo(HaveOccurred())

				routeInfo2, err := cc_messages.CCHTTPRoutes{
					{Hostname: "route-3"},
					{Hostname: "route-4"},
				}.CCRouteInfo()
				Expect(err).NotTo(HaveOccurred())

				desireRequests = []cc_messages.DesireAppRequestFromCC{
					{
						ProcessGuid:  processGuid1.String(),
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
						RoutingInfo:     routeInfo1,
						LogGuid:         "log-guid-1",
						ETag:            "1234567.1890",
					},
					{
						ProcessGuid:  processGuid2.String(),
						DropletUri:   "source-url-2",
						Stack:        "stack-2",
						StartCommand: "start-command-2",
						Environment: []*models.EnvironmentVariable{
							{Name: "env-key-3", Value: "env-value-3"},
							{Name: "env-key-4", Value: "env-value-4"},
						},
						MemoryMB:        512,
						DiskMB:          2048,
						FileDescriptors: 32,
						NumInstances:    4,
						RoutingInfo:     routeInfo2,
						LogGuid:         "log-guid-2",
						ETag:            "2345678.2901",
					},
					{
						ProcessGuid:     processGuid3.String(),
						DropletUri:      "source-url-3",
						Stack:           "stack-3",
						StartCommand:    "start-command-3",
						Environment:     []*models.EnvironmentVariable{},
						MemoryMB:        128,
						DiskMB:          512,
						FileDescriptors: 8,
						NumInstances:    4,
						RoutingInfo:     make(cc_messages.CCRouteInfo),
						LogGuid:         "log-guid-3",
						ETag:            "3456789.3012",
					},
				}

				fakeCC.AppendHandlers(
					ghttp.CombineHandlers(
						ghttp.VerifyRequest("POST", "/internal/bulk/apps"),
						ghttp.VerifyBasicAuth("the-username", "the-password"),
						ghttp.VerifyJSON(`[
							"`+processGuid1.String()+`",
							"`+processGuid2.String()+`"
						]
						`),
						ghttp.RespondWithJSONEncoded(200, desireRequests[:2]),
					),
					ghttp.CombineHandlers(
						ghttp.VerifyRequest("POST", "/internal/bulk/apps"),
						ghttp.VerifyBasicAuth("the-username", "the-password"),
						ghttp.VerifyJSON(`[
							"`+processGuid3.String()+`"
						]
						`),
						ghttp.RespondWithJSONEncoded(200, desireRequests[2:]),
					),
				)

				fingerprintsChan = make(chan []cc_messages.CCDesiredAppFingerprint)
			})

			It("gets desire app request messages for each fingerprints batch", func() {
				fingerprints := []cc_messages.CCDesiredAppFingerprint{
					cc_messages.CCDesiredAppFingerprint{
						ProcessGuid: processGuid1.String(),
						ETag:        "1234567.890",
					},
					cc_messages.CCDesiredAppFingerprint{
						ProcessGuid: processGuid2.String(),
						ETag:        "2345678.901",
					},
				}
				Eventually(fingerprintsChan).Should(BeSent(fingerprints))
				Eventually(resultsChan).Should(Receive(ConsistOf(desireRequests[:2])))

				fingerprints = []cc_messages.CCDesiredAppFingerprint{
					cc_messages.CCDesiredAppFingerprint{
						ProcessGuid: processGuid3.String(),
						ETag:        "3456789.012",
					},
				}
				Eventually(fingerprintsChan).Should(BeSent(fingerprints))
				Eventually(resultsChan).Should(Receive(ConsistOf(desireRequests[2])))

				close(fingerprintsChan)

				Eventually(resultsChan).Should(BeClosed())
				Eventually(errorsChan).Should(BeClosed())

				Expect(fakeCC.ReceivedRequests()).To(HaveLen(2))
			})
		})

		Context("when the fingerprint batch is empty", func() {
			BeforeEach(func() {
				fingerprintsChan <- []cc_messages.CCDesiredAppFingerprint{}
				close(fingerprintsChan)
			})

			It("does send a request to the CC", func() {
				Eventually(resultsChan).Should(BeClosed())
				Eventually(errorsChan).Should(BeClosed())

				Expect(fakeCC.ReceivedRequests()).To(HaveLen(0))
			})
		})

		Context("when the API times out", func() {
			ccResponseTime := 100 * time.Millisecond

			BeforeEach(func() {
				fakeCC.AppendHandlers(func(w http.ResponseWriter, req *http.Request) {
					time.Sleep(ccResponseTime)
					w.Write([]byte(`[]`))
				})

				httpClient = &http.Client{Timeout: ccResponseTime / 2}
				fingerprintsChan <- []cc_messages.CCDesiredAppFingerprint{
					{ProcessGuid: processGuid1.String(), ETag: "123"},
				}
			})

			It("sends an error on the error channel and closes the output channels", func() {
				Eventually(errorsChan).Should(Receive(BeAssignableToTypeOf(&url.Error{})))

				close(fingerprintsChan)
				Eventually(errorsChan).Should(BeClosed())
				Eventually(resultsChan).Should(BeClosed())
			})
		})

		Context("when the API returns an error response", func() {
			BeforeEach(func() {
				fakeCC.AppendHandlers(ghttp.RespondWith(403, ""))

				fingerprintsChan <- []cc_messages.CCDesiredAppFingerprint{
					{ProcessGuid: processGuid1.String(), ETag: "123"},
				}
			})

			It("sends an error on the error channel and closes the output channels", func() {
				Eventually(errorsChan).Should(Receive(MatchError(ContainSubstring("403"))))

				close(fingerprintsChan)
				Eventually(errorsChan).Should(BeClosed())
				Eventually(resultsChan).Should(BeClosed())
			})
		})

		Context("when the server responds with invalid JSON", func() {
			BeforeEach(func() {
				fakeCC.AppendHandlers(ghttp.RespondWith(200, "{"))

				fingerprintsChan <- []cc_messages.CCDesiredAppFingerprint{
					{ProcessGuid: processGuid1.String(), ETag: "123"},
				}
			})

			It("sends an error on the error channel and closes the output channels", func() {
				Eventually(errorsChan).Should(Receive(HaveOccurred()))

				close(fingerprintsChan)
				Eventually(errorsChan).Should(BeClosed())
				Eventually(resultsChan).Should(BeClosed())
			})
		})

		Describe("cancelling", func() {
			BeforeEach(func() {
				fakeCC.AppendHandlers(
					ghttp.CombineHandlers(
						ghttp.VerifyRequest("POST", "/internal/bulk/apps"),
						ghttp.RespondWithJSONEncoded(200, []cc_messages.DesireAppRequestFromCC{}),
					),
				)
			})

			Context("when waiting for fingerprints", func() {
				It("exits when cancelled", func() {
					close(cancel)

					Eventually(resultsChan).Should(BeClosed())
					Eventually(errorsChan).Should(BeClosed())
				})
			})

			Context("when waiting to send results", func() {
				BeforeEach(func() {
					fingerprintsChan <- []cc_messages.CCDesiredAppFingerprint{
						{ProcessGuid: processGuid1.String(), ETag: "an-etag"},
					}
				})

				It("exits when cancelled", func() {
					close(cancel)

					Eventually(resultsChan).Should(BeClosed())
					Eventually(errorsChan).Should(BeClosed())
				})
			})
		})
	})
})
